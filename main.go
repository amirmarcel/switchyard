package main

// Root entrypoint for switchyard.
//
// TODO: This is intentionally a stub. It becomes the real CLI once the workload
// generator and benchmark harness exist — i.e. when there is an actual
// "run <workload> through <backend> with <policy>" operation to expose
// (e.g. `switchyard sim --workload burst.yaml --policy fifo`). At that point it
// should move to cmd/switchyard/main.go per Go convention. Do not add logic here
// before then, and do not delete it — the module needs an entrypoint.
func main() {
}