// Package sentiment implements the sentiment calculation engine.
//
// The Engine orchestrates vote processing: trigger matching, debounce checks, rate limiting, and atomic vote application via Redis Functions.
// GetBroadcastData provides raw sentiment data with decay parameters for client-side computation.
package sentiment
