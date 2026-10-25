//go:build !windows

package discover

func windowsCwdsMap(pids []int) map[int]cwdInfo {
	return map[int]cwdInfo{}
}
