package main

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

//go:embed payload/*
var embedded embed.FS

var edition = "zhenguojian"

func main() {
	if err := launch(); err != nil {
		if len(os.Args) > 1 && os.Args[1] == "--package-smoke" {
			fmt.Fprintln(os.Stderr, err)
		} else {
			showError(err)
		}
		os.Exit(1)
	}
}

func launch() error {
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	home := filepath.Join(filepath.Dir(executable), ".zhenguojian")
	if err := safeDirectory(home); err != nil {
		return err
	}
	if err := os.MkdirAll(home, 0700); err != nil {
		return err
	}
	lock, err := instanceLock(filepath.Join(home, "instance.lock"))
	if err != nil {
		return errors.New("便携数据目录已被另一实例占用，请先关闭旧窗口")
	}
	defer windows.CloseHandle(lock)
	archive, err := embedded.ReadFile("payload/archive.zip")
	if err != nil {
		return errors.New("程序包缺少 Windows 运行文件")
	}
	digest := sha256.Sum256(archive)
	runtime := filepath.Join(home, "runtime", edition+"-"+hex.EncodeToString(digest[:8]))
	if err := prepareRuntime(archive, runtime); err != nil {
		return err
	}
	inner := filepath.Join(runtime, edition+".exe")
	if _, err := os.Stat(inner); err != nil {
		return errors.New("程序包缺少应用入口")
	}
	temporary := filepath.Join(home, "tmp")
	if err := safeDirectory(temporary); err != nil {
		return err
	}
	if err := os.MkdirAll(temporary, 0700); err != nil {
		return err
	}
	if err := migrateLegacy(home, inner, temporary); err != nil {
		return err
	}
	command := exec.Command(inner, os.Args[1:]...)
	command.Dir = runtime
	command.Env = portableEnvironment(home, temporary)
	command.Stdin = os.Stdin
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	return command.Run()
}

func portableEnvironment(home, temporary string) []string {
	environment := make([]string, 0, len(os.Environ())+3)
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if !strings.EqualFold(key, "ZJG_PORTABLE_HOME") && !strings.EqualFold(key, "TEMP") && !strings.EqualFold(key, "TMP") {
			environment = append(environment, entry)
		}
	}
	return append(environment, "ZJG_PORTABLE_HOME="+home, "TEMP="+temporary, "TMP="+temporary)
}

func instanceLock(name string) (windows.Handle, error) {
	pointer, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return 0, err
	}
	return windows.CreateFile(pointer, windows.GENERIC_READ|windows.GENERIC_WRITE, 0, nil, windows.OPEN_ALWAYS, windows.FILE_ATTRIBUTE_NORMAL, 0)
}

func safeDirectory(name string) error {
	info, err := os.Lstat(name)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("不是安全的应用目录：%s", name)
	}
	return nil
}

func prepareRuntime(archive []byte, runtime string) error {
	reader, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		return err
	}
	if len(reader.File) == 0 || len(reader.File) > 3000 {
		return errors.New("程序包文件数量异常")
	}
	var total uint64
	for _, file := range reader.File {
		clean := path.Clean(file.Name)
		if clean == "." || clean != strings.TrimSuffix(file.Name, "/") || strings.Contains(clean, "\\") || strings.HasPrefix(clean, "../") || strings.HasPrefix(clean, "/") || file.Mode()&os.ModeSymlink != 0 {
			return errors.New("程序包包含无效路径")
		}
		total += file.UncompressedSize64
		if total > 4<<30 {
			return errors.New("程序包展开大小异常")
		}
	}
	if runtimeValid(reader, runtime) {
		return nil
	}
	if err := safeDirectory(filepath.Dir(runtime)); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(runtime), 0700); err != nil {
		return err
	}
	staging := runtime + ".staging"
	if err := safeDirectory(staging); err != nil {
		return err
	}
	if err := os.RemoveAll(staging); err != nil {
		return err
	}
	if err := os.Mkdir(staging, 0700); err != nil {
		return err
	}
	for _, file := range reader.File {
		target := filepath.Join(staging, filepath.FromSlash(file.Name))
		if file.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0700); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
			return err
		}
		input, err := file.Open()
		if err != nil {
			return err
		}
		output, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			input.Close()
			return err
		}
		crc := crc32.NewIEEE()
		count, copyErr := io.Copy(io.MultiWriter(output, crc), input)
		closeErr := output.Close()
		input.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
		if uint64(count) != file.UncompressedSize64 || crc.Sum32() != file.CRC32 {
			return errors.New("程序包校验失败")
		}
	}
	if err := safeDirectory(runtime); err != nil {
		return err
	}
	if err := os.RemoveAll(runtime); err != nil {
		return err
	}
	return os.Rename(staging, runtime)
}

func runtimeValid(reader *zip.Reader, runtime string) bool {
	if err := safeDirectory(runtime); err != nil {
		return false
	}
	if _, err := os.Stat(runtime); err != nil {
		return false
	}
	for _, file := range reader.File {
		target := filepath.Join(runtime, filepath.FromSlash(file.Name))
		info, err := os.Lstat(target)
		if err != nil || info.Mode()&os.ModeSymlink != 0 {
			return false
		}
		if file.FileInfo().IsDir() {
			if !info.IsDir() {
				return false
			}
			continue
		}
		if !info.Mode().IsRegular() || uint64(info.Size()) != file.UncompressedSize64 {
			return false
		}
		input, err := os.Open(target)
		if err != nil {
			return false
		}
		crc := crc32.NewIEEE()
		_, err = io.Copy(crc, input)
		input.Close()
		if err != nil || crc.Sum32() != file.CRC32 {
			return false
		}
	}
	return true
}

func showError(err error) {
	name := "真果鉴"
	if edition == "hongguojian" {
		name = "红果鉴"
	}
	message, _ := windows.UTF16PtrFromString(name + "便携版无法启动：\n" + err.Error())
	title, _ := windows.UTF16PtrFromString(name)
	procedure := windows.NewLazySystemDLL("user32.dll").NewProc("MessageBoxW")
	procedure.Call(0, uintptr(unsafe.Pointer(message)), uintptr(unsafe.Pointer(title)), 0x10)
}
