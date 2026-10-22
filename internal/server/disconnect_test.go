package server

import "testing"

// Disconnecting a machine forgets the passwords remembered for it.
func TestDisconnectForgetsPasswords(t *testing.T) {
	e := newEnv(t)
	e.srv.prompts.mu.Lock()
	e.srv.prompts.cache["fake\x00senha:"] = "segredo"
	e.srv.prompts.cache["outro\x00senha:"] = "fica"
	e.srv.prompts.mu.Unlock()
	if c := e.call("POST", "/hosts/fake/disconnect", nil, nil); c != 200 {
		t.Fatalf("disconnect: %d", c)
	}
	e.srv.prompts.mu.Lock()
	defer e.srv.prompts.mu.Unlock()
	if _, ok := e.srv.prompts.cache["fake\x00senha:"]; ok {
		t.Fatal("a senha da máquina desconectada continua lembrada")
	}
	if e.srv.prompts.cache["outro\x00senha:"] != "fica" {
		t.Fatal("a senha de outra máquina foi esquecida")
	}
}
