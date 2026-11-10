//go:build windows

package clipboard

import "testing"

func TestWindowsCopyUsesDeferredNativeUnicodeClipboard(t *testing.T) {
	original := writeNativeClipboard
	t.Cleanup(func() { writeNativeClipboard = original })
	var copied string
	writeNativeClipboard = func(text string) error { copied = text; return nil }
	text := "zażółć gęślą jaźń\napiVersion: v1\n界"
	cmd := CopyCmd(text)
	if copied != "" {
		t.Fatal("clipboard I/O blocked the UI loop")
	}
	msg, ok := cmd().(CopyMsg)
	if !ok || !msg.Success || msg.Method != "native" || copied != text {
		t.Fatalf("native Windows clipboard was not used: %+v", msg)
	}
}
