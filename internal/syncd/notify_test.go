package syncd

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadReceiptsAreNotNewMail(t *testing.T) {
	dir := t.TempDir()
	receipt := filepath.Join(dir, "receipt")
	os.WriteFile(receipt, []byte("From: a@x\r\nSubject: Read: Hi\r\nContent-Type: multipart/report; report-type=disposition-notification; boundary=b\r\n\r\n--b--\r\n"), 0o600)
	plain := filepath.Join(dir, "plain")
	os.WriteFile(plain, []byte("From: a@x\r\nSubject: Hi\r\nContent-Type: multipart/report; report-type=delivery-status; boundary=b\r\n\r\n--b--\r\n"), 0o600)
	if !isReadReceipt(receipt) || isReadReceipt(plain) || isReadReceipt(filepath.Join(dir, "missing")) {
		t.Fatal("isReadReceipt is wrong")
	}
}
