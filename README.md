# js-admission-controller

This controller is an admission webhook with the following features:
- admssion rules (mutate, validate) are defined in CustomResourceDefinitions (CRD)
- CRD can be defined at cluster level (ClusterJsAdmissions) or namespace level (JsAdmissions)
- Mutations and validations are coded in javascript, allowing fast development and deployment

## TL;DR

```text
// actions
function jsa_mutate(op, obj, [sync], [state]) -> { Allowed: bool, Message: str, Result: obj }
function jsa_validate(op, obj, [sync], [state]) -> { Allowed: bool, Message: str }

// events
function jsa_init([state])
function jsa_created(obj, [sync], [state])
function jsa_updated(obj, old, [sync], [state])
function jsa_deleted(obj, [sync], [state])

// utils
function jsa_find(kind, namespace) -> [obj...]
function jsa_log(s...)
function jsa_logf(s...)
```

## Javascript specification

### Method names

All plugin methods are prefixed by `jsa_`.
It is recommended to avoid prefixing custom methods with jsa_ and consider it is reserved.

### Global variables

It is best to prevent use of global variables oto keep values across calls.
Use the `state` object for this.

## Methods to implement

For parameters:
- names are case-sensitive
- names in brackets like `[sync]` or `[state]` are optional
- order is not important: `jsa_validate(obj,sync)` and `jsa_validate(sync,obj)` will both work correctly

Optional parameters:
- sync: when present, method is called synchronized, with value set to true
- state: state object that can be used to keep data

### jsa_mutate(op, obj, [sync]) -> { Allowed: bool, Message: str, Result: obj }

Parameters:
- op: operation, one of CREATE, UPDATE, DELETE
- obj: the object, like a Pod or a Deployment

Result:
- Allowed: boolean
- Message: error message, only used when Allowed if false
- Result: altered object, only used when Allowed is true

The mutation will fail only and only if:
- the return value is not null or undefined
- the return value contains a field Allowed
- the value of Allowed is exactly false

In all other case, the mutation will succeed:
- if the field Result is present and not null or undefined, the mutation is computed by comparing obj and Result
- otherwise, no patch is applied

Mutation and patch are logged when at least one Allowed is returned with a non-empty patch.

### jsa_validate(op, obj, [sync]) -> { Allowed: bool, Message: str }

Parameters:
- op: operation, one of CREATE, UPDATE, DELETE
- obj: the object, like a Pod or a Deployment

Return value:
- Allowed: boolean
- Message: error message, only used when Allowed if false
- Result: altered object, only used when Allowed is true

The validation will fail only and only if:
- the return value is not null or undefined
- the return value contains a field Allowed
- the value of Allowed is exactly false

In all other case, the validation will succeed.

Validation is logged when at least one Allowed is returned.

### jsa_init([state])

> There is no `sync` parameter as this method is always called synchronized.

### jsa_created(obj, [sync])

Parameters:
- obj: the object created, like a Pod or a Deployment

There is no return value for this function.

### jsa_updated(obj, old, [sync])

Parameters:
- oldObj: the object before update, like a Pod or a Deployment
- newObj: the object after update, like a Pod or a Deployment

There is no return value for this function.

> Remember that updates are sent each time the resourceVersion field of the object is changed.
> This can happen when object is patched, but also when object status changes.

### jsa_deleted(obj, [sync])

Parameters:
- obj: the object created, like a Pod or a Deployment

## Examples

### Adding a new annotation to all pods

In this example we want to add a new annotation `annotation.io/test: 1` to all pods.

Here, we simply need to:
- implement `jsa_mutate` function to update the object

```js
// entrypoints
function jsa_mutate(op, obj) {
    if (op != "CREATE" || obj.kind !== "Pod") return;
    if (obj.metadata.annotations == null)
        obj.metadata.annotations = {};
    obj.metadata.annotations["jsadmissions.momiji.com/test"] = "1";
    return { Allowed: true, Result: obj };
}
```

### Limit the number of pods accross multiple namespaces

In this example we want to count the number of pods created across multiple namespaces, to prevent going above a limit of 40 pods.

Here, we would need to:
- implement `jsa_init` function to initialize the value
- implement `jsa_created` and `jsa_deleted` functions to update the value
- implement `jsa_validate` to test and eventually prevent object creation when limit is reached
- use `state` to store the number of existing pods
- use `sync` to be able to update this value atomically

```js
// entrypoints
function jsa_init(state) {
    state.podCount = 0;
}
function jsa_created(obj, sync, state) {
    // Check object kind and namespace
    if (!check(obj)) return;
    // Update state
    state.podCount++;
}
function jsa_deleted(obj, sync, state) {
    // Check object kind and namespace
    if (!check(obj)) return;
    // Update state
    state.podCount--;
}
function jsa_validate(op, obj, sync, state) {
    // Check object kind and namespace
    if (op != "CREATE" || !check(obj)) return;
    // Check pod count < limit
    if (state.podCount >= LIMIT) {
        return { Allowed: false, Message: "Max number of pods has been reached" };
    }
    return;
}
// custom code
var NAMESPACE_REGEX = /^default($|-)/;
var POD_LIMIT = 40;
function check(obj) {
    return obj.kind === "Pod" && obj.metadata.namespace.match(NAMESPACE_REGEX) != null;
}
```
