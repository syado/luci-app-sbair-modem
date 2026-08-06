// SPDX-License-Identifier: MIT
// Copyright (c) 2026 soralis0912

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"time"
)

// 時間のかかる処理の共通の足回り。

type jobState struct {
	State   string   `json:"state"` // running / done / error
	Step    string   `json:"step"`
	Message string   `json:"message,omitempty"`
	Started string   `json:"started,omitempty"`
	Target  int      `json:"target,omitempty"`  // simmap 用
	Mapping int      `json:"mapping,omitempty"` // simmap 用
	Stages  []string `json:"stages,omitempty"`  // download 用
}

type job struct {
	name  string // simmap / download
	state jobState
}

func jobPath(name string) string { return "/tmp/sbair-" + name + ".json" }

func newJob(name string) *job {
	return &job{name: name, state: jobState{
		State: "running", Step: "起動", Started: time.Now().Format(time.RFC3339),
	}}
}

func (j *job) write() {
	b, err := json.Marshal(j.state)
	if err != nil {
		return
	}
	// 部分的に書かれた JSON を読ませないよう、別名で書いてから置き換える。
	tmp := jobPath(j.name) + ".tmp"
	if os.WriteFile(tmp, b, 0644) == nil {
		_ = os.Rename(tmp, jobPath(j.name))
	}
}

func (j *job) step(s string) {
	j.state.Step = s
	j.write()
}

func (j *job) fail(step, msg string) int {
	j.state.State, j.state.Step, j.state.Message = "error", step, msg
	j.write()
	return 1
}

func (j *job) done(step, msg string) int {
	j.state.State, j.state.Step, j.state.Message = "done", step, msg
	j.write()
	return 0
}

func readJob(name string) map[string]any {
	b, err := os.ReadFile(jobPath(name))
	if err != nil {
		return map[string]any{"state": "idle"}
	}
	var m map[string]any
	if json.Unmarshal(b, &m) != nil {
		return map[string]any{"state": "idle"}
	}
	return m
}

// startJob spawns a detached copy of this program to run the work.
//
// **rpcd kills the process group when the ubus call returns**, so the worker
// has to leave it (Setsid). Without that a switch would be cut off partway -
// and partway means the radio is off and the mapping may already have changed.
func startJob(name string, args ...string) map[string]any {
	if cur := readJob(name); cur["state"] == "running" {
		return map[string]any{"error": "すでに実行中です", "state": "running"}
	}
	self, err := os.Executable()
	if err != nil {
		return map[string]any{"error": fmt.Sprintf("自分の場所が分かりません: %v", err)}
	}

	j := newJob(name)
	j.write()

	argv := append([]string{"-d", *device, name + "-worker"}, args...)
	cmd := exec.Command(self, argv...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, nil, nil
	if err := cmd.Start(); err != nil {
		j.fail("起動", err.Error())
		return map[string]any{"error": fmt.Sprintf("ワーカーを起動できません: %v", err)}
	}
	go func() { _ = cmd.Wait() }()

	return map[string]any{"result": "started"}
}
