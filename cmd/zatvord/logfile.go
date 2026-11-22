package main

// teeLog writes every line to each writer and ignores their errors: a
// missing console (stderr) must not keep lines from the file, nor the
// other way round.
type teeLog []interface{ Write([]byte) (int, error) }

func (t teeLog) Write(p []byte) (int, error) {
	for _, w := range t {
		_, _ = w.Write(p)
	}
	return len(p), nil
}
