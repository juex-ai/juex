//go:build windows

package tools

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	defaultPTYRows = 24
	defaultPTYCols = 80
)

func startPTYSession(cmd *exec.Cmd, session *shellSession) (io.WriteCloser, error) {
	inputRead, inputWrite, err := createWindowsPipe()
	if err != nil {
		return nil, err
	}
	outputRead, outputWrite, err := createWindowsPipe()
	if err != nil {
		_ = windows.CloseHandle(inputRead)
		_ = windows.CloseHandle(inputWrite)
		return nil, err
	}

	var pseudoConsole windows.Handle
	if err := windows.CreatePseudoConsole(windows.Coord{X: defaultPTYCols, Y: defaultPTYRows}, inputRead, outputWrite, 0, &pseudoConsole); err != nil {
		_ = windows.CloseHandle(inputRead)
		_ = windows.CloseHandle(inputWrite)
		_ = windows.CloseHandle(outputRead)
		_ = windows.CloseHandle(outputWrite)
		return nil, fmt.Errorf("exec_command: create ConPTY: %w", err)
	}
	_ = windows.CloseHandle(inputRead)
	_ = windows.CloseHandle(outputWrite)

	inputFile := os.NewFile(uintptr(inputWrite), "juex-conpty-input")
	outputFile := os.NewFile(uintptr(outputRead), "juex-conpty-output")
	if inputFile == nil || outputFile == nil {
		if inputFile != nil {
			_ = inputFile.Close()
		}
		if outputFile != nil {
			_ = outputFile.Close()
		}
		windows.ClosePseudoConsole(pseudoConsole)
		return nil, fmt.Errorf("exec_command: create ConPTY files")
	}

	attrList, err := windows.NewProcThreadAttributeList(1)
	if err != nil {
		_ = inputFile.Close()
		_ = outputFile.Close()
		windows.ClosePseudoConsole(pseudoConsole)
		return nil, err
	}
	pseudoConsoleValue := pseudoConsole
	if err := attrList.Update(
		windows.PROC_THREAD_ATTRIBUTE_PSEUDOCONSOLE,
		unsafe.Pointer(&pseudoConsoleValue),
		unsafe.Sizeof(pseudoConsoleValue),
	); err != nil {
		attrList.Delete()
		_ = inputFile.Close()
		_ = outputFile.Close()
		windows.ClosePseudoConsole(pseudoConsole)
		return nil, err
	}

	startupInfo := windows.StartupInfoEx{}
	startupInfo.StartupInfo.Cb = uint32(unsafe.Sizeof(startupInfo))
	startupInfo.ProcThreadAttributeList = attrList.List()

	commandLine, err := windows.UTF16PtrFromString(windowsCommandLine(cmd.Args))
	if err != nil {
		attrList.Delete()
		_ = inputFile.Close()
		_ = outputFile.Close()
		windows.ClosePseudoConsole(pseudoConsole)
		return nil, err
	}
	var currentDir *uint16
	if cmd.Dir != "" {
		currentDir, err = windows.UTF16PtrFromString(cmd.Dir)
		if err != nil {
			attrList.Delete()
			_ = inputFile.Close()
			_ = outputFile.Close()
			windows.ClosePseudoConsole(pseudoConsole)
			return nil, err
		}
	}

	var processInfo windows.ProcessInformation
	if err := windows.CreateProcess(
		nil,
		commandLine,
		nil,
		nil,
		false,
		windows.EXTENDED_STARTUPINFO_PRESENT,
		nil,
		currentDir,
		&startupInfo.StartupInfo,
		&processInfo,
	); err != nil {
		attrList.Delete()
		_ = inputFile.Close()
		_ = outputFile.Close()
		windows.ClosePseudoConsole(pseudoConsole)
		return nil, err
	}
	_ = windows.CloseHandle(processInfo.Thread)

	go func() {
		defer outputFile.Close()
		_, _ = io.Copy(shellSessionWriter{session: session}, outputFile)
	}()

	var processMu sync.Mutex
	processHandle := processInfo.Process
	var cleanupOnce sync.Once
	cleanup := func() {
		cleanupOnce.Do(func() {
			processMu.Lock()
			h := processHandle
			processHandle = 0
			processMu.Unlock()

			attrList.Delete()
			windows.ClosePseudoConsole(pseudoConsole)
			_ = inputFile.Close()
			if h != 0 {
				_ = windows.CloseHandle(h)
			}
		})
	}

	session.waitFunc = func() error {
		processMu.Lock()
		h := processHandle
		processMu.Unlock()
		if h == 0 {
			return nil
		}
		_, waitErr := windows.WaitForSingleObject(h, windows.INFINITE)
		var exitCode uint32
		if waitErr == nil {
			waitErr = windows.GetExitCodeProcess(h, &exitCode)
		}
		cleanup()
		if waitErr != nil {
			return waitErr
		}
		if exitCode != 0 {
			return &shellExitCodeError{code: int(exitCode)}
		}
		return nil
	}
	session.killFunc = func() error {
		processMu.Lock()
		h := processHandle
		if h == 0 {
			processMu.Unlock()
			return nil
		}
		err := windows.TerminateProcess(h, 1)
		processMu.Unlock()
		if err != nil {
			return err
		}
		_ = inputFile.Close()
		return nil
	}

	return inputFile, nil
}

func createWindowsPipe() (windows.Handle, windows.Handle, error) {
	var read windows.Handle
	var write windows.Handle
	if err := windows.CreatePipe(&read, &write, nil, 0); err != nil {
		return 0, 0, err
	}
	return read, write, nil
}

func windowsCommandLine(args []string) string {
	if len(args) == 0 {
		return ""
	}
	escaped := make([]string, len(args))
	for i, arg := range args {
		escaped[i] = windows.EscapeArg(arg)
	}
	return strings.Join(escaped, " ")
}
