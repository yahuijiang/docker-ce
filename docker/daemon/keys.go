// +build linux

package daemon // import "github.com/docker/docker/daemon"

import (
	"fmt"
	"io/ioutil"
	"os"
	"strconv"
	"strings"
)

const (
	rootKeyFile   = "/proc/sys/kernel/keys/root_maxkeys"
	rootBytesFile = "/proc/sys/kernel/keys/root_maxbytes"
	rootKeyLimit  = 1000000
	// it is standard configuration to allocate 25 bytes per key
	rootKeyByteMultiplier = 25
)

// ModifyRootKeyLimit checks to see if the root key limit is set to
// at least 1000000 and changes it to that limit along with the maxbytes
// allocated to the keys at a 25 to 1 multiplier.
// 读取系统中用户可以拥有的最大的key 数量以及每个key大小的限额。
// 并且设置每个key 大小限额的限制设置为目前系统的25呗
func ModifyRootKeyLimit() error {
	value, err := readRootKeyLimit(rootKeyFile)
	if err != nil {
		return err
	}
	if value < rootKeyLimit {
		return setRootKeyLimit(rootKeyLimit)
	}
	return nil
}

func setRootKeyLimit(limit int) error {
	keys, err := os.OpenFile(rootKeyFile, os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	defer keys.Close()
	if _, err := fmt.Fprintf(keys, "%d", limit); err != nil {
		return err
	}
	bytes, err := os.OpenFile(rootBytesFile, os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	defer bytes.Close()
	_, err = fmt.Fprintf(bytes, "%d", limit*rootKeyByteMultiplier)
	return err
}

func readRootKeyLimit(path string) (int, error) {
	data, err := ioutil.ReadFile(path)
	if err != nil {
		return -1, err
	}
	return strconv.Atoi(strings.Trim(string(data), "\n"))
}
