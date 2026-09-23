package main

import (
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/sys/windows"
)

type migrationState struct {
	Source         string `json:"source"`
	Cache          string `json:"cache,omitempty"`
	External       string `json:"external,omitempty"`
	SourceDigest   string `json:"sourceDigest"`
	CacheDigest    string `json:"cacheDigest,omitempty"`
	ExternalDigest string `json:"externalDigest,omitempty"`
	Cleanup        bool   `json:"cleanup"`
}

type sourceFile struct {
	relative string
	length   int64
	digest   string
}

func migrateLegacy(home, inner, temporary string) error {
	roaming, err := windows.KnownFolderPath(windows.FOLDERID_RoamingAppData, 0)
	if err != nil {
		return err
	}
	local, err := windows.KnownFolderPath(windows.FOLDERID_LocalAppData, 0)
	if err != nil {
		return err
	}
	return migrateLegacyFrom(home, inner, temporary,
		filepath.Join(roaming, "真果鉴", "真果鉴"), filepath.Join(local, "真果鉴", "真果鉴"))
}

func migrateLegacyFrom(home, inner, temporary, source, cache string) error {
	data := filepath.Join(home, "data")
	staging := filepath.Join(home, "data.migrating")
	marker := filepath.Join(home, "migration.pending.json")
	if overlaps(source, home) || overlaps(cache, home) || overlaps(source, cache) {
		return errors.New("便携目录与旧应用数据目录重叠")
	}
	if err := safeDirectory(data); err != nil {
		return err
	}
	state, err := readMigration(marker)
	if err != nil {
		return err
	}
	if state != nil {
		if !strings.EqualFold(filepath.Clean(state.Source), filepath.Clean(source)) ||
			state.Cache != "" && !strings.EqualFold(filepath.Clean(state.Cache), filepath.Clean(cache)) ||
			state.External != "" && (!strings.EqualFold(filepath.Base(state.External), "zhenguojian-downloads") || overlaps(state.External, home) || overlaps(state.External, source) || overlaps(state.External, cache)) {
			return errors.New("旧数据迁移记录无效，已停止清理")
		}
		if _, err := os.Stat(data); os.IsNotExist(err) {
			if err := safeDirectory(staging); err != nil {
				return err
			}
			if err := os.RemoveAll(staging); err != nil {
				return err
			}
			if err := os.Remove(marker); err != nil {
				return err
			}
			state = nil
		} else if err != nil {
			return err
		}
	}
	if state == nil {
		if _, err := os.Stat(data); err == nil {
			return nil
		} else if !os.IsNotExist(err) {
			return err
		}
		sourcePresent, err := directoryPresent(source)
		if err != nil {
			return err
		}
		cachePresent, err := directoryPresent(cache)
		if err != nil {
			return err
		}
		if !sourcePresent && !cachePresent {
			return nil
		}
	}
	if err := oldApplicationClosed(); err != nil {
		return err
	}
	if state == nil {
		state, err = stageMigration(source, cache, data, staging, marker)
		if err != nil {
			return err
		}
	}
	probe := exec.Command(inner, "--portable-probe")
	probe.Dir = filepath.Dir(inner)
	probe.Env = portableEnvironment(home, temporary)
	if err := probe.Run(); err != nil {
		return errors.New("新目录中的配置或下载记录无法初始化，旧数据已保留")
	}
	retiredSource := state.Source + ".portable-migrated"
	retiredCache := ""
	if state.Cache != "" {
		retiredCache = state.Cache + ".portable-migrated"
	}
	retiredExternal := ""
	if state.External != "" {
		retiredExternal = state.External + ".portable-migrated"
	}
	if !state.Cleanup {
		if state.SourceDigest != "" {
			if err := retireLegacy(state.Source, retiredSource, state.SourceDigest); err != nil {
				return fmt.Errorf("迁移期间旧配置发生变化，旧数据已保留：%w", err)
			}
		}
		if state.Cache != "" {
			if err := retireLegacy(state.Cache, retiredCache, state.CacheDigest); err != nil {
				return fmt.Errorf("迁移期间旧缓存发生变化，旧数据已保留：%w", err)
			}
		}
		if state.External != "" {
			if err := retireLegacy(state.External, retiredExternal, state.ExternalDigest); err != nil {
				return fmt.Errorf("迁移期间旧下载发生变化，旧数据已保留：%w", err)
			}
		}
		state.Cleanup = true
		if err := writeMigration(marker, state); err != nil {
			return err
		}
	}
	if state.SourceDigest != "" {
		if err := removeLegacyDirectory(retiredSource); err != nil {
			return fmt.Errorf("便携数据已验证，但清理旧配置失败：%w", err)
		}
	}
	if state.Cache != "" {
		if err := removeLegacyDirectory(retiredCache); err != nil {
			return fmt.Errorf("便携数据已验证，但清理旧缓存失败：%w", err)
		}
	}
	if state.External != "" {
		if err := removeLegacyDirectory(retiredExternal); err != nil {
			return fmt.Errorf("便携数据已验证，但清理旧下载失败：%w", err)
		}
	}
	_ = os.Remove(filepath.Dir(state.Source))
	if state.Cache != "" {
		_ = os.Remove(filepath.Dir(state.Cache))
	}
	return os.Remove(marker)
}

func stageMigration(source, cache, data, staging, marker string) (*migrationState, error) {
	sourcePresent, err := directoryPresent(source)
	if err != nil {
		return nil, err
	}
	cachePresent, err := directoryPresent(cache)
	if err != nil {
		return nil, err
	}
	var files, cacheFiles []sourceFile
	var sourceDigest, cacheDigest string
	var sourceBytes, cacheBytes int64
	if sourcePresent {
		files, sourceDigest, sourceBytes, err = scanDirectory(source)
		if err != nil {
			return nil, err
		}
	}
	if cachePresent {
		cacheFiles, cacheDigest, cacheBytes, err = scanDirectory(cache)
		if err != nil {
			return nil, err
		}
	}
	downloads := filepath.Join(source, "downloads")
	external := ""
	rewriteLocation := false
	location, err := os.ReadFile(filepath.Join(source, "download-location.json"))
	if err == nil {
		var value struct {
			Directory string `json:"directory"`
		}
		if json.Unmarshal(location, &value) == nil && filepath.IsAbs(value.Directory) {
			rewriteLocation = true
			if !strings.EqualFold(filepath.Clean(value.Directory), filepath.Clean(downloads)) {
				external = filepath.Clean(value.Directory)
			}
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	nestedSource := descendant(external, source)
	nestedCache := descendant(external, cache)
	if external != "" && (!strings.EqualFold(filepath.Base(external), "zhenguojian-downloads") ||
		overlaps(external, data) || overlaps(external, source) && !nestedSource || overlaps(external, cache) && !nestedCache) {
		return nil, errors.New("旧下载目录不是独立的应用目录，已停止迁移")
	}
	var externalFiles []sourceFile
	var externalDigest string
	var externalBytes int64
	if external != "" {
		externalFiles, externalDigest, externalBytes, err = scanDirectory(external)
		if err != nil {
			return nil, err
		}
	}
	available, err := freeBytes(filepath.Dir(data))
	if err != nil {
		return nil, err
	}
	if sourceBytes+cacheBytes+externalBytes > int64(available)-(64<<20) {
		return nil, errors.New("便携目录空间不足，旧数据未修改")
	}
	if err := safeDirectory(staging); err != nil {
		return nil, err
	}
	if err := os.RemoveAll(staging); err != nil {
		return nil, err
	}
	if err := os.Mkdir(staging, 0700); err != nil {
		return nil, err
	}
	if sourcePresent {
		if err := copyVerified(source, staging, files); err != nil {
			return nil, err
		}
	}
	if cachePresent {
		if err := copyVerified(cache, filepath.Join(staging, "platform-cache"), cacheFiles); err != nil {
			return nil, err
		}
	}
	if external != "" {
		oldDefault := filepath.Join(staging, "downloads")
		if _, err := os.Stat(oldDefault); err == nil {
			if err := os.Rename(oldDefault, filepath.Join(staging, "legacy-downloads")); err != nil {
				return nil, err
			}
		} else if !os.IsNotExist(err) {
			return nil, err
		}
		if err := copyVerified(external, filepath.Join(staging, "downloads"), externalFiles); err != nil {
			return nil, err
		}
	}
	if rewriteLocation {
		body, _ := json.Marshal(map[string]string{"directory": filepath.Join(data, "downloads")})
		if err := os.WriteFile(filepath.Join(staging, "download-location.json"), body, 0600); err != nil {
			return nil, err
		}
	}
	state := &migrationState{Source: source, SourceDigest: sourceDigest}
	if external != "" && !nestedSource && !nestedCache {
		state.External = external
		state.ExternalDigest = externalDigest
	}
	if cachePresent {
		state.Cache = cache
		state.CacheDigest = cacheDigest
	}
	if err := writeMigration(marker, state); err != nil {
		return nil, err
	}
	if err := os.Rename(staging, data); err != nil {
		return nil, err
	}
	return state, nil
}

func directoryPresent(name string) (bool, error) {
	if err := safeDirectory(name); err != nil {
		return false, err
	}
	_, err := os.Stat(name)
	if os.IsNotExist(err) {
		return false, nil
	}
	return err == nil, err
}

func scanDirectory(root string) ([]sourceFile, string, int64, error) {
	if err := safeDirectory(root); err != nil {
		return nil, "", 0, err
	}
	var files []sourceFile
	var bytes int64
	aggregate := sha256.New()
	err := filepath.WalkDir(root, func(name string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, err := os.Lstat(name)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() && !info.Mode().IsRegular() {
			return errors.New("旧数据包含链接或特殊文件，已停止迁移")
		}
		if info.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(root, name)
		if err != nil {
			return err
		}
		digest, err := fileDigest(name)
		if err != nil {
			return err
		}
		files = append(files, sourceFile{relative: relative, length: info.Size(), digest: digest})
		bytes += info.Size()
		io.WriteString(aggregate, relative+"\x00"+strconv.FormatInt(info.Size(), 10)+"\x00"+digest+"\n")
		return nil
	})
	if err != nil {
		return nil, "", 0, err
	}
	return files, hex.EncodeToString(aggregate.Sum(nil)), bytes, nil
}

func directoryDigest(root string) (string, error) {
	_, digest, _, err := scanDirectory(root)
	return digest, err
}

func fileDigest(name string) (string, error) {
	file, err := os.Open(name)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hasher := sha256.New()
	if _, err := io.Copy(hasher, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

func copyVerified(source, target string, files []sourceFile) error {
	if err := os.MkdirAll(target, 0700); err != nil {
		return err
	}
	for _, entry := range files {
		outputPath := filepath.Join(target, entry.relative)
		if err := os.MkdirAll(filepath.Dir(outputPath), 0700); err != nil {
			return err
		}
		input, err := os.Open(filepath.Join(source, entry.relative))
		if err != nil {
			return err
		}
		output, err := os.OpenFile(outputPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			input.Close()
			return err
		}
		hasher := sha256.New()
		written, copyErr := io.Copy(io.MultiWriter(output, hasher), input)
		syncErr := output.Sync()
		closeErr := output.Close()
		input.Close()
		if copyErr != nil {
			return copyErr
		}
		if syncErr != nil {
			return syncErr
		}
		if closeErr != nil {
			return closeErr
		}
		if written != entry.length || hex.EncodeToString(hasher.Sum(nil)) != entry.digest {
			return errors.New("旧数据复制时发生变化，原文件已保留")
		}
		if digest, err := fileDigest(outputPath); err != nil || digest != entry.digest {
			return errors.New("新数据校验失败，原文件已保留")
		}
	}
	return nil
}

func freeBytes(path string) (uint64, error) {
	pointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	var available, total, free uint64
	if err := windows.GetDiskFreeSpaceEx(pointer, &available, &total, &free); err != nil {
		return 0, err
	}
	return available, nil
}

func overlaps(first, second string) bool {
	a := strings.ToLower(filepath.Clean(first))
	b := strings.ToLower(filepath.Clean(second))
	return a == b || strings.HasPrefix(a, b+string(os.PathSeparator)) || strings.HasPrefix(b, a+string(os.PathSeparator))
}

func descendant(child, parent string) bool {
	return strings.HasPrefix(strings.ToLower(filepath.Clean(child)),
		strings.ToLower(filepath.Clean(parent))+string(os.PathSeparator))
}

func oldApplicationClosed() error {
	result, err := exec.Command("tasklist", "/FO", "CSV", "/NH").Output()
	if err != nil {
		return errors.New("无法确认旧程序是否关闭，已停止迁移")
	}
	rows, err := csv.NewReader(strings.NewReader(string(result))).ReadAll()
	if err != nil {
		return errors.New("无法读取运行中的程序，已停止迁移")
	}
	for _, row := range rows {
		if len(row) < 2 || !strings.EqualFold(row[0], "zhenguojian.exe") && !strings.EqualFold(row[0], "hongguojian.exe") {
			continue
		}
		pid, err := strconv.Atoi(row[1])
		if err == nil && pid != os.Getpid() {
			return errors.New("请先关闭旧版真果鉴或红果鉴，再启动便携版")
		}
	}
	return nil
}

func readMigration(name string) (*migrationState, error) {
	body, err := os.ReadFile(name)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var state migrationState
	if json.Unmarshal(body, &state) != nil || state.Source == "" || state.SourceDigest == "" && state.CacheDigest == "" || state.Cache != "" && state.CacheDigest == "" {
		return nil, errors.New("旧数据迁移记录损坏，旧数据已保留")
	}
	return &state, nil
}

func writeMigration(name string, state *migrationState) error {
	body, err := json.Marshal(state)
	if err != nil {
		return err
	}
	temporary := name + ".tmp"
	if err := os.WriteFile(temporary, body, 0600); err != nil {
		return err
	}
	return os.Rename(temporary, name)
}

func removeLegacyDirectory(name string) error {
	if err := safeDirectory(name); err != nil {
		return err
	}
	return os.RemoveAll(name)
}

func retireLegacy(source, retired, expected string) error {
	name := source
	if _, err := os.Stat(retired); err == nil {
		name = retired
	} else if !os.IsNotExist(err) {
		return err
	}
	actual, err := directoryDigest(name)
	if err != nil || actual != expected {
		return errors.New("源文件校验失败")
	}
	if name == retired {
		return nil
	}
	return os.Rename(source, retired)
}
