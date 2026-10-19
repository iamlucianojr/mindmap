package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTheSameQuestionIsTheSameJob(t *testing.T) {
	path := []string{"root", "5 Attack", "the outage rule"}
	a := JobID(path, "what does this mean?")
	b := JobID(path, "what does this mean?")
	if a != b {
		t.Fatalf("asking twice minted two jobs: %s and %s", a, b)
	}
	if c := JobID(path, "what does this mean, really?"); c == a {
		t.Fatal("rewording the question reused the old job")
	}
	if c := JobID([]string{"root", "6 Gate"}, "what does this mean?"); c == a {
		t.Fatal("the same words under a different node reused the old job")
	}
}

// The path is joined with a separator the labels cannot contain, so two
// different trees cannot collide into one id.
func TestPathSegmentsCannotBeConfused(t *testing.T) {
	if JobID([]string{"a", "b"}, "q") == JobID([]string{"ab"}, "q") {
		t.Fatal("a/b and ab produced the same id")
	}
}

func TestAnOrderIdCannotEscapeItsDirectory(t *testing.T) {
	for _, bad := range []string{"", "../../etc/passwd", "a/b", "a.b", "a b", strings.Repeat("x", 41)} {
		if _, err := safeName(bad); err == nil {
			t.Errorf("accepted %q as an order id", bad)
		}
	}
	for _, good := range []string{"a1b2c3d4", "WO-001", "under_score"} {
		if _, err := safeName(good); err != nil {
			t.Errorf("rejected %q: %v", good, err)
		}
	}
}

func TestABriefRefusesToGuessTheRepo(t *testing.T) {
	o := &Orders{dir: t.TempDir(), briefs: t.TempDir()}
	if _, err := o.Brief(OrderReq{ID: "a1b2c3d4", Label: "x"}); err == nil {
		t.Fatal("wrote a brief with nowhere for the work to land")
	}
	if _, err := o.Brief(OrderReq{ID: "", Repo: "r", Label: "x"}); err == nil {
		t.Fatal("wrote a brief with no node id")
	}
}

func TestAMissingDescriptionIsNamedNotInvented(t *testing.T) {
	o := &Orders{dir: t.TempDir(), briefs: t.TempDir()}
	p, err := o.Brief(OrderReq{ID: "a1b2c3d4", Repo: "mindmap", Label: "Do the thing"})
	if err != nil {
		t.Fatal(err)
	}
	body := readFile(t, p)
	if !strings.Contains(body, "<UNKNOWN") {
		t.Error("a brief with no description did not say so")
	}
	if !strings.Contains(body, "[KEY]") {
		t.Error("a missing ticket key was filled in rather than marked")
	}
	if !strings.Contains(body, "Post nothing, anywhere") {
		t.Error("the publish gate is missing from the brief")
	}
}

func TestABriefCarriesTheDescription(t *testing.T) {
	o := &Orders{dir: t.TempDir(), briefs: t.TempDir()}
	const want = "Give the dispatcher a gRPC server."
	if err := o.SetDescription("a1b2c3d4", want); err != nil {
		t.Fatal(err)
	}
	p, err := o.Brief(OrderReq{ID: "a1b2c3d4", Repo: "saas", Label: "Entry"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(readFile(t, p), want) {
		t.Fatal("the description written on the node did not reach the brief")
	}
}

// A brief edited by hand outranks one this generator would produce.
func TestAnExistingBriefIsNotOverwritten(t *testing.T) {
	o := &Orders{dir: t.TempDir(), briefs: t.TempDir()}
	req := OrderReq{ID: "a1b2c3d4", Repo: "saas", Label: "Entry"}
	p, err := o.Brief(req)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, p, "edited by a human")
	if _, err := o.Brief(req); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, p); got != "edited by a human" {
		t.Fatalf("the generator clobbered an edited brief: %q", got)
	}
}

func TestTheDocumentLogSurvivesReopening(t *testing.T) {
	path := filepath.Join(t.TempDir(), "log.jsonl")
	d, err := OpenDoc(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Save("mindmap\n  root((a))", "first"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Save("mindmap\n  root((b))", "second"); err != nil {
		t.Fatal(err)
	}

	again, err := OpenDoc(path)
	if err != nil {
		t.Fatal(err)
	}
	last := again.Latest()
	if last.N != 2 || !strings.Contains(last.Src, "root((b))") {
		t.Fatalf("reopened log lost the latest revision: %+v", last)
	}
	if len(again.History()) != 2 {
		t.Fatalf("history has %d entries, want 2", len(again.History()))
	}
	if again.History()[0].Src != "" {
		t.Error("history shipped the whole document when it only needs the list")
	}
}

func TestAJobIsOnDiskBeforeItIsAnswered(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jobs.jsonl")
	q, err := OpenQueue(path, NewHub(), 1)
	if err != nil {
		t.Fatal(err)
	}
	j := q.Submit("ask", "n1", "why?", []string{"root", "n1"})
	if j.State != StateQueued {
		t.Fatalf("a fresh job was %q, want queued", j.State)
	}

	// Reopened with no worker ever having run: the job must still be there.
	again, err := OpenQueue(path, NewHub(), 1)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := again.Get(j.ID)
	if !ok {
		t.Fatal("the job was not written before it ran")
	}
	if got.Prompt != "why?" {
		t.Fatalf("job came back as %+v", got)
	}
}

func TestAnInterruptedJobIsAskedAgain(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jobs.jsonl")
	q, err := OpenQueue(path, NewHub(), 1)
	if err != nil {
		t.Fatal(err)
	}
	j := q.Submit("ask", "n1", "why?", nil)
	q.set(j.ID, func(j *Job) { j.State = StateRunning }) // as if the process died here

	again, err := OpenQueue(path, NewHub(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if n := again.Resume(); n != 1 {
		t.Fatalf("resume re-queued %d jobs, want 1", n)
	}
	if got, _ := again.Get(j.ID); got.State != StateQueued {
		t.Fatalf("interrupted job is %q, want queued", got.State)
	}
}

func TestAFinishedJobIsNotAskedAgain(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jobs.jsonl")
	q, _ := OpenQueue(path, NewHub(), 1)
	j := q.Submit("ask", "n1", "why?", nil)
	q.set(j.ID, func(j *Job) { j.State = StateDone; j.Answer = "because" })

	again, _ := OpenQueue(path, NewHub(), 1)
	if n := again.Resume(); n != 0 {
		t.Fatalf("resume re-ran %d finished jobs, want 0", n)
	}
	if got, _ := again.Get(j.ID); got.Answer != "because" {
		t.Fatalf("the answer was lost on reopen: %+v", got)
	}
}

func TestAJobRunsToAnAnswer(t *testing.T) {
	q, _ := OpenQueue(filepath.Join(t.TempDir(), "jobs.jsonl"), NewHub(), 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	q.Start(ctx, FakeModel{}, 1)

	j := q.Submit("ask", "n1", "why?", []string{"root", "n1"})
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if got, _ := q.Get(j.ID); got.State == StateDone {
			if got.Answer == "" {
				t.Fatal("job finished with an empty answer")
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	got, _ := q.Get(j.ID)
	t.Fatalf("job never finished, last state %q err %q", got.State, got.Err)
}

func TestTheQuestionCarriesWhereItWasAsked(t *testing.T) {
	p := Prompt{Question: "what does this mean?", Path: []string{"root", "5 Attack", "the outage rule"}}
	text := p.Text()
	if !strings.Contains(text, "5 Attack") || !strings.Contains(text, "the outage rule") {
		t.Fatalf("the trail is missing from the prompt:\n%s", text)
	}
	bare := Prompt{Question: "hello"}
	if strings.Contains(bare.Text(), ">") {
		t.Fatal("a question with no trail grew one anyway")
	}
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func writeFile(t *testing.T, p, s string) {
	t.Helper()
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}
