package main

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

func exportOne(t *testing.T, content string) (*Local, []byte) {
	t.Helper()
	src := &Local{Home: t.TempDir()}
	writeAtomic(src.abs("data/dump.sql"), []byte(content), 0o600)
	var buf bytes.Buffer
	if err := src.Export(ExportReq{BaseRel: "data", Paths: []string{"dump.sql"}}, &buf); err != nil {
		t.Fatal(err)
	}
	return &Local{Home: t.TempDir()}, buf.Bytes()
}

func TestImportChecksWhatArrived(t *testing.T) {
	content := strings.Repeat("row\n", 300)
	dst, stream := exportOne(t, content)
	if err := dst.Import(ImportReq{BaseRel: "in"}, bytes.NewReader(stream)); err != nil {
		t.Fatalf("clean stream refused: %v", err)
	}

	start := bytes.Index(stream, []byte(content[:40]))
	bad := append([]byte{}, stream...)
	bad[start] ^= 1 // first byte of the file's contents
	err := dst.Import(ImportReq{BaseRel: "in", Clean: true}, bytes.NewReader(bad))
	if err == nil || !strings.Contains(err.Error(), "arrived different") {
		t.Fatalf("corrupted stream: %v", err)
	}

	cut := stream[:start+((len(content)+511)/512)*512] // headers and contents, no sums
	err = dst.Import(ImportReq{BaseRel: "in", Clean: true}, bytes.NewReader(cut))
	if err == nil || !strings.Contains(err.Error(), "before its checksums") {
		t.Fatalf("truncated stream: %v", err)
	}
}

func TestInterruptedTransferResumes(t *testing.T) {
	src := &Local{Home: t.TempDir()}
	dst := &Local{Home: t.TempDir()}
	content := strings.Repeat("0123456789abcdef", 64<<10) // 1 MB
	writeAtomic(src.abs("src/app/.ferry/state/db.dump"), []byte(content), 0o600)
	sha, _ := fileSHA(src.abs("src/app/.ferry/state/db.dump"))
	entry := FileEntry{Path: ".ferry/state/db.dump", Size: int64(len(content)), SHA: sha}
	ex := ExportReq{BaseRel: "src/app", Paths: []string{entry.Path}, SHAs: map[string]string{entry.Path: sha}}
	im := ImportReq{BaseRel: "stage", Private: true}

	var full bytes.Buffer
	if err := src.Export(ex, &full); err != nil {
		t.Fatal(err)
	}
	// The link dies 600 KB into the file.
	if err := dst.Import(im, bytes.NewReader(full.Bytes()[:600<<10])); err == nil {
		t.Fatal("a cut stream was accepted")
	}
	plan, err := dst.PlanExtras(PlanReq{WtRel: "src/app", StageRel: "stage", Files: []FileEntry{entry}})
	if err != nil {
		t.Fatal(err)
	}
	off := plan.Partial[entry.Path]
	if off < 500<<10 {
		t.Fatalf("plan should resume near where it stopped, got offset %d", off)
	}

	ex.Offsets = plan.Partial
	var rest bytes.Buffer
	if err := src.Export(ex, &rest); err != nil {
		t.Fatal(err)
	}
	if int64(rest.Len()) > int64(len(content))-off+8<<10 {
		t.Fatalf("resumed transfer sent %d bytes; %d were left", rest.Len(), int64(len(content))-off)
	}
	if err := dst.Import(im, &rest); err != nil {
		t.Fatalf("resume failed: %v", err)
	}
	if _, err := dst.InstallExtras(InstallExtrasReq{WtRel: "src/app", StageRel: "stage", Task: "t", Files: []FileEntry{entry}}); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(dst.abs("src/app/.ferry/state/db.dump")); string(got) != content {
		t.Fatal("resumed file differs from the original")
	}
}

func TestOnlyListedExtrasAreInstalled(t *testing.T) {
	l := &Local{Home: t.TempDir()}
	writeAtomic(l.abs("stage/.env"), []byte("new\n"), 0o600)
	writeAtomic(l.abs("stage/.env.old"), []byte("left by an earlier attempt\n"), 0o600)
	os.MkdirAll(l.abs("src/app"), 0o755)
	if _, err := l.InstallExtras(InstallExtrasReq{WtRel: "src/app", StageRel: "stage", Task: "t", Files: []FileEntry{{Path: ".env"}}}); err != nil {
		t.Fatal(err)
	}
	if !exists(l.abs("src/app/.env")) || exists(l.abs("src/app/.env.old")) {
		t.Fatal("installed something other than the listed extras")
	}
}
