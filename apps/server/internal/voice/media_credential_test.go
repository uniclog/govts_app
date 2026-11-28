package voice

import "testing"

func TestMediaCredentialStableForSessionAndRejectedForAnother(t *testing.T) {
	hub := NewHub()
	alice, err := hub.CreateSession("alice", nil)
	if err != nil {
		t.Fatal(err)
	}
	bob, err := hub.CreateSession("bob", nil)
	if err != nil {
		t.Fatal(err)
	}
	first, err := hub.MediaCredential(alice.ID)
	if err != nil {
		t.Fatal(err)
	}
	second, err := hub.MediaCredential(alice.ID)
	if err != nil {
		t.Fatal(err)
	}
	if first == ([32]byte{}) || first != second {
		t.Fatal("credential is empty or unstable")
	}
	if !hub.AuthenticateMedia(alice.ID, first) {
		t.Fatal("valid credential rejected")
	}
	if hub.AuthenticateMedia(bob.ID, first) {
		t.Fatal("credential accepted for another session")
	}
	hub.Remove(alice.ID)
	if hub.AuthenticateMedia(alice.ID, first) {
		t.Fatal("removed session credential accepted")
	}
}
