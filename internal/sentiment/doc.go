// Package sentiment implements the sentiment calculation engine.
//
// The Engine orchestrates vote processing: trigger matching, debounce checks, rate limiting, and atomic vote application via Redis Functions.
// GetCurrentValue computes time-decayed sentiment. No mutable state (delegates to Redis).
package sentiment
