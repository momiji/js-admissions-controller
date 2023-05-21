package main

import (
	"context"
	"fmt"
	"github.com/momiji/js-admission-controller/admission"
	"github.com/momiji/js-admission-controller/discovery"
	"github.com/momiji/js-admission-controller/logs"
	"github.com/momiji/js-admission-controller/utils"
	"github.com/momiji/js-admission-controller/watcher"
	"github.com/spf13/pflag"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"net"
	"net/http"
	"os"
	"os/signal"
	"regexp"
	"strings"
	"syscall"
)

var (
	clusterConfig     *rest.Config
	clusterClient     *dynamic.DynamicClient
	discoveryClient   *discovery.Discovery
	admissions        *admission.Admissions
	resourcesWatcher  *watcher.Watcher
	admissionsWatcher *watcher.Watcher
)

func main() {
	var err error

	// vars
	var tlsKey, tlsCert string
	var version, help bool
	var ip net.IP
	var port int

	// flags
	pflag.IPVar(&ip, "ip", net.ParseIP("0.0.0.0"), "Bind address IP")
	pflag.IntVar(&port, "port", 8043, "Bind address Port")
	pflag.StringVar(&tlsCert, "tlsCert", "/etc/certs/tls.crt", "Path to the TLS certificate")
	pflag.StringVar(&tlsKey, "tlsKey", "/etc/certs/tls.key", "Path to the TLS key")
	pflag.BoolVarP(&version, "version", "V", false, "Show version")
	pflag.BoolVarP(&help, "help", "h", false, "Show help")
	pflag.BoolVarP(&logs.DebugMode, "verbose", "v", false, "Verbose mode (with javascript logs)")
	pflag.BoolVarP(&logs.TraceMode, "debug", "d", false, "Debug mode (all logs))")

	// env
	re := regexp.MustCompile("_[a-z]")
	replace := func(s string) string { return strings.ToUpper(s[1:]) }
	for _, v := range os.Environ() {
		if strings.HasPrefix(v, "ENV_JSA_") {
			vals := strings.SplitN(v, "=", 2)
			flagName := strings.ToLower(vals[0][8:])
			flagName = re.ReplaceAllStringFunc(flagName, replace)
			fn := pflag.CommandLine.Lookup(flagName)
			if fn != nil {
				if err = fn.Value.Set(vals[1]); err != nil {
					logs.Errorf("Invalid value for %s", v)
				}
			}
		}
	}

	// parse
	pflag.Parse()
	if help {
		pflag.Usage()
		os.Exit(0)
	}

	// loading kube config
	kubeConfig := os.Getenv("KUBECONFIG")
	if kubeConfig != "" {
		clusterConfig, err = clientcmd.BuildConfigFromFlags("", kubeConfig)
	} else {
		clusterConfig, err = rest.InClusterConfig()
	}
	if err != nil {
		logs.Fatalf("Unable to load kube config", err)
	}

	// create client
	clusterClient, err = dynamic.NewForConfig(clusterConfig)
	if err != nil {
		logs.Fatalf("Unable to create kube client", err)
	}

	// create discovery
	discoveryClient, err = discovery.NewDiscovery(clusterConfig)
	if err != nil {
		logs.Fatalf("Unable to create discovery client", err)
	}

	// create admissions
	admissions = admission.NewAdmissions()

	// create cancellable context
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// listen shutdown signal
	go func() {
		signalChan := make(chan os.Signal, 1)
		signal.Notify(signalChan, syscall.SIGINT, syscall.SIGTERM)
		sig := <-signalChan
		logs.Warnf("Received %s signal; shutting down...", sig)
		cancel()
	}()

	// create watcher for resources, not for CRD
	logs.Infof("Start watching non-CRD resources")
	resourcesWatcher = watcher.NewWatcher(ctx, clusterClient, resourceHandler)

	// create watcher and load resources
	logs.Infof("Start watching CRD resources")
	admissionsWatcher = watcher.NewWatcher(ctx, clusterClient, admissionHandler)

	// load cluster CRD
	gvr, err := discoveryClient.GetGVRFromResource(ClusterCrd)
	if err != nil {
		logs.Fatalf("%v", err)
	}
	err = admissionsWatcher.Add(gvr)
	if err != nil {
		logs.Fatalf("%v", err)
	}

	// load namespace CRD
	gvr, err = discoveryClient.GetGVRFromResource(NamespaceCrd)
	if err != nil {
		logs.Fatalf("%v", err)
	}
	err = admissionsWatcher.Add(gvr)
	if err != nil {
		logs.Fatalf("%v", err)
	}

	// start webhook server
	go func() {
		logs.Infof("Start webhook server")
		http.HandleFunc("/mutate", serveMutate)
		http.HandleFunc("/validate", serveValidate)
		addr := fmt.Sprintf("%s:%d", ip, port)
		err = http.ListenAndServeTLS(addr, tlsCert, tlsKey, nil)
		if err != nil {
			logs.Fatalf("Unable to start webhook server: %v", err)
		}
	}()

	// wait forever
	<-ctx.Done()
	os.Exit(0)
}

func admissionHandler(action int, obj *unstructured.Unstructured, old *unstructured.Unstructured) {
	gvk := utils.GVKToString(obj.GroupVersionKind())
	ns := obj.GetNamespace()
	name := obj.GetName()
	content := obj.UnstructuredContent()
	js, _, _ := unstructured.NestedString(content, "spec", "js")
	kinds, _, _ := unstructured.NestedStringSlice(content, "spec", "kinds")

	//
	res := make([]string, 0)
	watch := make([]schema.GroupVersionResource, 0)
	for _, kind := range kinds {
		kr, err := discoveryClient.GetGVRFromResource(kind)
		if err != nil {
			logs.Errorf("CRD %s %s: invalid resource %s", gvk, name, kind)
		}
		kk, err := discoveryClient.GetGVKFromResource(kind)
		if err != nil {
			logs.Errorf("CRD %s %s: invalid kind %s", gvk, name, kind)
		}
		res = append(res, utils.GVKToString(kk))
		watch = append(watch, kr)
	}

	// delete admission
	if action == watcher.DELETED {
		admissions.Remove(ns, name)
		return
	}

	logs.Infof("Admissions: add %s ns=%s name=%s kinds=%v", gvk, ns, name, res)

	// watch new resources
	for _, resource := range watch {
		resourcesWatcher.Add(resource)
	}

	// we need to lock all resources to prevent losing them during initialisation from informer
	for _, resource := range res {
		resourcesWatcher.LockResource(resource)
		defer resourcesWatcher.UnlockResource(resource)
	}

	// create or update admission
	adm := &admission.Admission{
		Namespace:  ns,
		Name:       name,
		Resources:  res,
		Javascript: js,
	}
	code, err := admissions.Upsert(adm)
	if err != nil {
		logs.Errorf("Admissions: failed to add s ns=%s name=%s kinds=%v: %v", gvk, ns, name, res, err)
	}

	// initialise new admission
	err = code.Init()
	if err != nil {
		logs.Errorf("Admissions: failed to init() s ns=%s name=%s kinds=%v: %v", gvk, ns, name, res, err)
	}

	// call created for all resources
	// synced by design as resources are locked, preventing resourceHandler to run
	for _, resource := range res {
		for _, obj := range resourcesWatcher.GetResources(resource, code.Admission.Namespace) {
			err = code.Created(obj)
			if err != nil {
				logs.Errorf("Admissions: failed to created() s ns=%s name=%s kinds=%v: %v", gvk, ns, name, res, err)
			}
		}
	}
}

func resourceHandler(action int, obj *unstructured.Unstructured, old *unstructured.Unstructured) {
	gvk := utils.GVKToString(obj.GroupVersionKind())
	for _, code := range admissions.Find(gvk, obj.GetNamespace()) {
		switch action {
		case watcher.CREATED:
			code.Created(obj)
		case watcher.UPDATED:
			code.Updated(obj, old)
		case watcher.DELETED:
			code.Deleted(obj)
		}
	}
}
