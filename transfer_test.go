package main

import (
	"bytes"
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

	bad := append([]byte{}, stream...)
	bad[512] ^= 1 // first byte of the file's contents
	err := dst.Import(ImportReq{BaseRel: "in", Clean: true}, bytes.NewReader(bad))
	if err == nil || !strings.Contains(err.Error(), "arrived different") {
		t.Fatalf("corrupted stream: %v", err)
	}

	cut := stream[:512+((len(content)+511)/512)*512] // header and contents, no sums
	err = dst.Import(ImportReq{BaseRel: "in", Clean: true}, bytes.NewReader(cut))
	if err == nil || !strings.Contains(err.Error(), "before its checksums") {
		t.Fatalf("truncated stream: %v", err)
	}
}
