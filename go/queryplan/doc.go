// Package queryplan contains Golem's immutable, provider-neutral query-plan
// diagnostic report.
//
// A report is evidence about a prepared typed read, not database authority. It
// deliberately has no SQL, bind, physical-name, cost, estimate, row, timing,
// provider-setting, or raw-provider-detail field. Version 1 is bounded to 32
// statements, 256 nodes, node depth 32, 64 field identities per node, 64 total
// warnings, and 256 KiB of private canonical encoding. Reports can only be
// produced through Golem's validated internal planning boundary.
package queryplan
