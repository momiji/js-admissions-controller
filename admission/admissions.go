package admission

import (
	"fmt"
	"sort"
	"sync"

	admission "k8s.io/api/admission/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

type Admissions struct {
	mux sync.RWMutex
	//clustered  *AdmissionList
	namespaces map[string]*AdmissionList
}

type Admission struct {
	Namespace  string
	Name       string
	Resources  []string
	Javascript string
	Timeout    int
}

type AdmissionList struct {
	admissions map[string]*AdmissionCode
}
type AdmissionCode struct {
	Admission *Admission
	Context   *JsContext
	IsValid   bool
}

func NewAdmissions() *Admissions {
	return &Admissions{
		mux: sync.RWMutex{},
		//clustered:  newAdmissionList(),
		namespaces: make(map[string]*AdmissionList),
	}
}

func newAdmissionList() *AdmissionList {
	return &AdmissionList{
		admissions: make(map[string]*AdmissionCode),
	}
}

func newAdmissionCode(adm *Admission) (*AdmissionCode, error) {
	js, err := NewJsContext(adm.FullName(), adm.Javascript, adm.Timeout)
	if err != nil {
		return nil, err
	}
	return &AdmissionCode{
		Admission: adm,
		Context:   js,
		IsValid:   false,
	}, nil
}

func (a *Admission) FullName() string {
	if a.Namespace == "" {
		return a.Name
	}
	return fmt.Sprintf("%s.%s", a.Namespace, a.Name)
}

func (a *Admissions) Upsert(adm *Admission) (*AdmissionCode, error) {
	a.mux.Lock()
	defer a.mux.Unlock()

	// get list
	var ok bool
	list, ok := a.namespaces[adm.Namespace]
	if !ok {
		list = newAdmissionList()
		a.namespaces[adm.Namespace] = list
	}

	// create code
	delete(list.admissions, adm.Name)
	code, err := newAdmissionCode(adm)
	if err != nil {
		return nil, err
	}

	// add code
	list.admissions[adm.Name] = code
	return code, nil
}

func (a *Admissions) Remove(namespace string, name string) {
	a.mux.Lock()
	defer a.mux.Unlock()

	// get list
	list, ok := a.namespaces[namespace]
	if !ok {
		return
	}

	// delete code
	delete(list.admissions, name)
}

// Find returns admissions for current namespace and cluster if namespace != "".
//
// For a namespace resource (like pods), all admissions for this namespace and for the cluster are returned.
// For a cluster resource (like clusterroles), only admissions for the cluster are returned.
func (a *Admissions) Find(resource string, namespace string) []*AdmissionCode {
	//TODO potential optimization? put a cache in place
	a.mux.RLock()
	defer a.mux.RUnlock()

	// list all namespace
	codes := make([]*AdmissionCode, 0)
	if list, ok := a.namespaces[namespace]; ok {
		for _, code := range list.admissions {
			if code.IsValid {
				for _, r := range code.Admission.Resources {
					if r == resource {
						codes = append(codes, code)
						break
					}
				}
			}
		}
	}
	if namespace != "" {
		if list, ok := a.namespaces[""]; ok {
			for _, code := range list.admissions {
				if code.IsValid {
					for _, r := range code.Admission.Resources {
						if r == resource {
							codes = append(codes, code)
							break
						}
					}
				}
			}
		}
	}

	// sort to have namespace then cluster, and sort by name
	sort.Slice(codes, func(i int, j int) bool {
		if codes[i].Admission.Namespace != codes[j].Admission.Namespace {
			return codes[i].Admission.Namespace > codes[j].Admission.Namespace
		}
		return codes[i].Admission.Name <= codes[j].Admission.Name
	})

	return codes
}

func (c *AdmissionCode) Init() error {
	ctx := c.Context
	_, err := ctx.Call(JsaInit, true, map[string]interface{}{"state": &ctx.State})
	if err != nil {
		return err
	}
	return nil
}

func (c *AdmissionCode) Created(obj *unstructured.Unstructured) error {
	ctx := c.Context
	_, err := ctx.Call(JsaCreated, false, map[string]interface{}{"state": &ctx.State, "sync": true, "obj": obj.Object})
	if err != nil {
		return err
	}
	return nil
}

func (c *AdmissionCode) Updated(obj *unstructured.Unstructured, old *unstructured.Unstructured) error {
	ctx := c.Context
	_, err := ctx.Call(JsaUpdated, false, map[string]interface{}{"state": &ctx.State, "sync": true, "obj": obj.Object, "old": old.Object})
	if err != nil {
		return err
	}
	return nil
}

func (c *AdmissionCode) Deleted(obj *unstructured.Unstructured) error {
	ctx := c.Context
	_, err := ctx.Call(JsaDeleted, false, map[string]interface{}{"state": &ctx.State, "sync": true, "obj": obj.Object})
	if err != nil {
		return err
	}
	return nil
}

func (c *AdmissionCode) Validate(operation admission.Operation, obj *unstructured.Unstructured) (*unstructured.Unstructured, error) {
	ctx := c.Context
	res, err := ctx.Call(JsaValidate, false, map[string]interface{}{"state": &ctx.State, "sync": true, "obj": obj.Object, "op": operation})
	if err != nil {
		return nil, err
	}
	if res == nil {
		return nil, nil
	}
	return ToUnstructured(res.Export()), nil
}

func (c *AdmissionCode) Mutate(operation admission.Operation, obj *unstructured.Unstructured) (*unstructured.Unstructured, error) {
	ctx := c.Context
	res, err := ctx.Call(JsaMutate, false, map[string]interface{}{"state": &ctx.State, "sync": true, "obj": obj.Object, "op": operation})
	if err != nil {
		return nil, err
	}
	if res == nil {
		return nil, nil
	}
	return ToUnstructured(res.Export()), nil
}
