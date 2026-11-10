package services

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"
)

func TestAuditArchiveTraversalAndSymlinksRejected(t *testing.T) {
	svc := &UpdateServiceImpl{}
	for _, name := range []string{"../escape", filepath.VolumeName(t.TempDir()) + string(os.PathSeparator) + "absolute", "../../argonaut"} {
		if _, err := secureExtractPath(t.TempDir(), name); err == nil {
			t.Fatalf("unsafe entry accepted: %q", name)
		}
	}
	var packed bytes.Buffer
	gz := gzip.NewWriter(&packed)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: "argonaut", Typeflag: tar.TypeSymlink, Linkname: "../escape"}); err != nil {
		t.Fatal(err)
	}
	tw.Close()
	gz.Close()
	if err := svc.extractTarGz(bytes.NewReader(packed.Bytes()), t.TempDir()); err == nil {
		t.Fatal("symlink archive accepted")
	}
	t.Log("PASS: updater rejects path traversal, absolute paths, and symlink entries")
}

func TestAuditBinarySelectionAcceptsWrongSubstring(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "argonaut-not-the-binary"), []byte("audit-dummy"), 0755); err != nil {
		t.Fatal(err)
	}
	chosen, err := (&UpdateServiceImpl{}).findBinaryInDir(dir, "argonaut")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(chosen) != "argonaut-not-the-binary" {
		t.Fatal("behavior changed: substring no longer accepted")
	}
	t.Log("OBSERVED: updater accepts a filename merely containing argonaut")
}
