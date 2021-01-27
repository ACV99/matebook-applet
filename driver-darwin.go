// Copyright (C) 2021 Evgeny Kuznetsov (evgeny@kuznetsov.md)
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU General Public License for more details.
//
// You should have received a copy of the GNU General Public License
// along with this program. If not, see <https://www.gnu.org/licenses/>.

// +build darwin

package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/nicksnyder/go-i18n/v2/i18n"
)

func initEndpoints() {
	for _, val := range os.Environ() {
		if strings.HasPrefix(val, "PATH") {
			logTrace.Println(val)
		}
	}
	threshEndpoints = append(threshEndpoints, threshDriver{splitThreshEndpoint{ioioGetter{}, ioioSetter{}}}, threshDriver{splitThreshEndpoint{zeroGetter{}, errSetter{}}})
	fnlockEndpoints = append(fnlockEndpoints, splitFnlockEndpoint{ioioFnGetter{}, ioioFnSetter{}})
	config.wait = true
}

// splitFnlockEndpoint is an fnlockEndpoint that has really differing
// ways of reading and writing
type splitFnlockEndpoint struct {
	getter fnlockGetter
	setter fnlockSetter
}

func (sfe splitFnlockEndpoint) toggle() {
	state, err := sfe.get()
	if err != nil {
		logError.Println(localizer.MustLocalize(&i18n.LocalizeConfig{MessageID: "CantReadFnlock"}))
		return
	}
	if err = sfe.setter.set(!state); err != nil {
		logError.Println(localizer.MustLocalize(&i18n.LocalizeConfig{MessageID: "CantToggleFnlock"}))
	}
	if config.wait {
		logTrace.Println("FnLock state pushed, will wait for it to be set")
		// driver takes some time to set values due to ACPI bug
		for i := 1; i < 5; i++ {
			time.Sleep(900 * time.Millisecond)
			logTrace.Println("checking state, attempt", i)
			newState, err := sfe.get()
			if newState != state && err == nil {
				logTrace.Println("state set as expected")
				break
			}
			logTrace.Println("not set yet")
		}
		logTrace.Println("alright, going on")
	}
}

func (sfe splitFnlockEndpoint) get() (bool, error) {
	return sfe.getter.get()
}

func (sfe splitFnlockEndpoint) isWritable() bool {
	state, err := sfe.get()
	if err != nil {
		return false
	}
	if err = sfe.setter.set(state); err != nil {
		return false
	}
	return true
}

// fnlockGetter has a way to read fnlock state
type fnlockGetter interface {
	get() (bool, error)
}

// fnlockSetter has a way to set fnlock state
type fnlockSetter interface {
	set(bool) error
}

// ioioFnGetter is a crude and suboptimal fnlockGetter using ioio and system log
type ioioFnGetter struct{}

func (_ ioioFnGetter) get() (state bool, err error) {
	cmdIoio := exec.Command("ioio", "-s", "org_rehabman_ACPIDebug", "dbg6", "0")
	var got string
	got, err = readFromLog(cmdIoio)
	if err != nil {
		return
	}

	return getFnlockFromLog(got)
}

// ioioFnSetter is an fnlockSetter that uses ioio
type ioioFnSetter struct{}

func (_ ioioFnSetter) set(state bool) error {
	logTrace.Printf("Using ioio to set FnLock state to %v", state)
	arg := "65536" // 0x10000
	if state {
		arg = "131072" // 0x20000
	}
	cmd := exec.Command("ioio", "-s", "org_rehabman_ACPIDebug", "dbg7", arg)
	err := cmd.Run()
	return err
}

// splitThreshEndpoint is a wmiDriver that has really differing
// ways of reading and writing
type splitThreshEndpoint struct {
	getter threshGetter
	setter threshSetter
}

func (ste splitThreshEndpoint) write(min, max int) error {
	return ste.setter.set(min, max)
}

func (ste splitThreshEndpoint) get() (min, max int, err error) {
	return ste.getter.get()
}

// threshGetter has a way to read battery thresholds
type threshGetter interface {
	get() (min, max int, err error)
}

// threshSetter has a way to set battery thresholds
type threshSetter interface {
	set(min, max int) error
}

// zeroGetter always returns 0 100
type zeroGetter struct{}

func (_ zeroGetter) get() (min, max int, err error) {
	return 0, 100, nil
}

// errSetter always returs error
type errSetter struct{}

func (_ errSetter) set(_, _ int) error {
	return fmt.Errorf("not implemented")
}

// ioioGetter is a crude and suboptimal threshGetter using ioio and system log
type ioioGetter struct{}

func (_ ioioGetter) get() (min, max int, err error) {
	cmdIoio := exec.Command("ioio", "-s", "org_rehabman_ACPIDebug", "dbg4", "0")
	var got string
	got, err = readFromLog(cmdIoio)
	if err != nil {
		return
	}

	return getThreshFromLog(got)
}

// ioioSetter is a threshSetter that uses ioio
type ioioSetter struct{}

func (_ ioioSetter) set(min, max int) error {
	logTrace.Printf("Using ioio to set battery thresholds to %d-%d", min, max)
	return ioioThreshSet(min, max)
}

func ioioThreshSet(min, max int) error {
	arg := threshToHexArg(min, max)
	cmd := exec.Command("ioio", "-s", "org_rehabman_ACPIDebug", "dbg5", arg)
	err := cmd.Run()
	return err
}

func readFromLog(cmdIoio *exec.Cmd) (result string, err error) {
	cmdLog := exec.Command("log", "stream", "--predicate", "senderImagePath contains \"ACPIDebug\"")
	var out bytes.Buffer
	cmdLog.Stdout = &out

	err = cmdLog.Start()
	if err != nil {
		logError.Println(localizer.MustLocalize(&i18n.LocalizeConfig{DefaultMessage: &i18n.Message{ID: "CantRunLog", Other: "Failed to run the \"log\" command. Is \"log\" binary not in PATH?"}}))
		return
	}
	defer func() { _ = cmdLog.Process.Signal(os.Kill) }()
	defer logTrace.Println("Killing the log output process...")
	time.Sleep(200 * time.Millisecond)
	err = cmdIoio.Run()
	if err != nil {
		logError.Println(localizer.MustLocalize(&i18n.LocalizeConfig{DefaultMessage: &i18n.Message{ID: "CantRunIoio", Other: "Failed to run the \"ioio\" command. Is the binary not in PATH?"}}))
		return
	}
	time.Sleep(500 * time.Millisecond)

	result = out.String()
	logTrace.Printf("Read from the log: %s", result)
	return
}

func threshToHexArg(min, max int) string {
	argHex := fmt.Sprintf("%02x%02x0000", max, min)
	arg, err := strconv.ParseInt(argHex, 16, 32)
	if err != nil {
		return "0"
	}
	return strconv.FormatInt(arg, 10)
}

func getThreshFromLog(log string) (min, max int, err error) {
	fail := fmt.Errorf("failed to parse log fragment")

	l := strings.Split(log, "Reading (hexadecimal values):")
	if len(l) < 2 {
		err = fail
		return
	}
	l = strings.Split(l[1], ",")

	val, err := getHexValues(l, 2)
	if err != nil {
		return
	}
	return val[0], val[1], nil
}

func getHexValues(l []string, num int) (values []int, err error) {
	fail := fmt.Errorf("failed to parse hex value(s)")
	val := []int{}
	for _, s := range l {
		i := strings.Index(s, "x")
		if i == -1 {
			continue
		}
		if len(s) < i+2 {
			err = fail
			return
		}
		logTrace.Printf("Found hex value:%s", s)
		var v int64
		h := s[i+1:]
		v, err = strconv.ParseInt(h, 16, 32)
		if err != nil {
			err = fail
			return
		}
		val = append(val, int(v))
	}
	if len(val) != num {
		err = fail
		return
	}
	return val, nil
}

func getFnlockFromLog(log string) (state bool, err error) {
	fail := fmt.Errorf("failed to parse log fragment")

	l := strings.Split(log, "Reading Fn-Lock state")
	if len(l) < 2 {
		err = fail
		return
	}
	l = strings.Split(l[1], ",")
	val, err := getHexValues(l, 1)
	if err != nil {
		return
	}
	return val[0] != 0, nil
}
