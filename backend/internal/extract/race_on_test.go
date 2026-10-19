//go:build race

package extract

// raceEnabled is true when tests run under the race detector, which makes
// compiling the PDF engine about ten times slower (20 s instead of 2 s).
const raceEnabled = true
