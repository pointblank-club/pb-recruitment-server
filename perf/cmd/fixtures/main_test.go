package main

import "testing"

func TestOwnership(t *testing.T) {
	valid := Manifest{RunID: "pbperf-20261106T144500Z-smoke", Contest: "pbperf-20261106T144500Z-smoke", Users: []User{{UID: "pbperf-20261106T144500Z-smoke-u0000"}}}
	owned(valid)
	for _, m := range []Manifest{{RunID: "recruitment"}, {RunID: valid.RunID, Contest: "real-contest"}, {RunID: valid.RunID, Users: []User{{UID: "candidate"}}}} {
		func() {
			defer func() {
				if recover() == nil {
					t.Error("foreign resource allowed")
				}
			}()
			owned(m)
		}()
	}
}
