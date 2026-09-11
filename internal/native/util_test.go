package native

import "testing"

func TestScrubbedEnvPreservesWindowsLocalAppData(t *testing.T) {
	const value = `C:\Users\runner\AppData\Local`
	t.Setenv("LOCALAPPDATA", value)

	want := "LOCALAPPDATA=" + value
	for _, entry := range scrubbedEnv() {
		if entry == want {
			return
		}
	}
	t.Fatalf("scrubbed environment omitted %q", want)
}
