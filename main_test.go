package main

import "testing"

func TestReleaseSelfTest(t *testing.T) {
	if err := runSelfTest(); err != nil {
		t.Fatal(err)
	}
}
