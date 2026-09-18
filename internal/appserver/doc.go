// Package appserver provides the narrow, stdio-only Codex App Server adapter
// used by codexbot agent workers.
//
// The package intentionally exposes typed operations rather than a generic
// JSON-RPC Call method. This keeps the set of methods an agent worker can send
// small and auditable. The wire shapes in this package target Codex CLI 0.153.4.
package appserver
