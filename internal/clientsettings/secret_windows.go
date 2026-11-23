//go:build windows

package clientsettings

import (
	"encoding/base64"
	"errors"
	"unsafe"

	"golang.org/x/sys/windows"
)

// ProtectSecret encrypts a server password for the current Windows user with
// DPAPI, so settings.json copied to another account or machine does not reveal it.
func ProtectSecret(secret string) (string, error) {
	if secret == "" {
		return "", nil
	}
	data := []byte(secret)
	in := windows.DataBlob{Size: uint32(len(data)), Data: &data[0]}
	var out windows.DataBlob
	if err := windows.CryptProtectData(&in, nil, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out); err != nil {
		return "", err
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))
	return "dpapi:" + base64.StdEncoding.EncodeToString(unsafe.Slice(out.Data, out.Size)), nil
}

// UnprotectSecret reverses ProtectSecret.
func UnprotectSecret(value string) (string, error) {
	if value == "" {
		return "", nil
	}
	if len(value) < 6 || value[:6] != "dpapi:" {
		return "", errors.New("unknown secret format")
	}
	data, err := base64.StdEncoding.DecodeString(value[6:])
	if err != nil || len(data) == 0 {
		return "", errors.New("invalid protected secret")
	}
	in := windows.DataBlob{Size: uint32(len(data)), Data: &data[0]}
	var out windows.DataBlob
	if err := windows.CryptUnprotectData(&in, nil, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out); err != nil {
		return "", err
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))
	return string(unsafe.Slice(out.Data, out.Size)), nil
}
