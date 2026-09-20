package setup

import (
	"io"
	"os"
	"testing"
	"time"
)

// runSetup feeds input to the wizard through a pipe and fails the test if the
// wizard writes a config file or fails to return promptly. Callers use this to
// assert that EOF / quit paths never persist configuration (and therefore never
// touch MySQL or .mcp.json).
func runSetup(t *testing.T, input string) {
	t.Helper()

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	oldStdin := os.Stdin
	os.Stdin = r
	defer func() { os.Stdin = oldStdin }()

	savePath := findSetupConfigPath()
	if _, statErr := os.Stat(savePath); statErr == nil {
		_ = os.Remove(savePath)
	}

	go func() {
		_, _ = io.WriteString(w, input)
		_ = w.Close()
	}()

	done := make(chan struct{})
	go func() {
		Run()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("setup.Run did not return promptly")
	}
	_ = r.Close()

	if _, statErr := os.Stat(savePath); statErr == nil {
		_ = os.Remove(savePath)
		t.Fatalf("setup wrote a config file for input %q", input)
	}
}

// TestRun_QuitsCleanlyWithoutWritingConfig drives the wizard through the
// language prompt and then quits at the first step. It must return promptly
// and must not save a config file (and therefore must not touch MySQL).
func TestRun_QuitsCleanlyWithoutWritingConfig(t *testing.T) {
	runSetup(t, "2\nq\n")
}

// TestRun_EOFAtLanguageDoesNotWriteConfig covers empty stdin: the very first
// scan hits EOF and the wizard must abort before any prompt defaults are used.
func TestRun_EOFAtLanguageDoesNotWriteConfig(t *testing.T) {
	runSetup(t, "")
}

// TestRun_EOFAtMiddleStepDoesNotWriteConfig covers truncated stdin after the
// first two steps. EOF at a yes/no prompt must abort the wizard.
func TestRun_EOFAtMiddleStepDoesNotWriteConfig(t *testing.T) {
	runSetup(t, "2\n\n\n")
}

// TestRun_QuitAtYesNoPromptDoesNotWriteConfig covers an explicit "q" at a
// yes/no prompt, which previously fell through to the default answer.
func TestRun_QuitAtYesNoPromptDoesNotWriteConfig(t *testing.T) {
	runSetup(t, "2\n\n\nq\n")
}

// TestRun_EOFAtConfirmationDoesNotWriteConfig walks every step with valid
// input (including a MySQL DSN) and then hits EOF at the save confirmation.
// Without an explicit confirmation nothing may be written.
func TestRun_EOFAtConfirmationDoesNotWriteConfig(t *testing.T) {
	runSetup(t, "2\n"+"\n"+"\n"+"y\n"+"user:pw@tcp(127.0.0.1:3306)/knowledge_mcp\n"+
		"n\n"+"n\n"+"\n"+"n\n"+"n\n"+"n\n"+"\n"+"\n"+"\n"+"\n")
}

// TestRun_MissingMySQLIsNotSaved walks the wizard to the end with MySQL
// disabled and confirms "y". MySQL is a hard startup requirement, so no config
// may be saved and the wizard must not claim success.
func TestRun_MissingMySQLIsNotSaved(t *testing.T) {
	runSetup(t, "2\n"+"\n"+"\n"+"n\n"+
		"n\n"+"n\n"+"\n"+"n\n"+"n\n"+"n\n"+"\n"+"\n"+"\n"+"\n"+"y\n")
}
