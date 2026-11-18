package domain

import "testing"

func TestQuotaUnitsAndMissingUsage(t *testing.T) {
	in := map[string]any{"UserQuota:1": map[string]any{"domain": "school", "gpu": 100, "gpuUsed": 0, "memory": 6144, "appCount": 1}, "UserStorageQuota:2": map[string]any{"storageName": "home", "fileStorageQuotaInMb": 2048, "fileStorageUsedInMb": 1024}}
	m := CapacityFromQuota(in)
	seen := map[string]string{}
	for _, r := range m {
		seen[r.Scope+"/"+r.Resource] = r.Amounts()
	}
	for k, v := range map[string]string{"school/GPU": "0 / 1", "school/实例": "— / 1", "school/内存": "— / 6 GiB", "home/文件存储": "1 / 2 GiB"} {
		if seen[k] != v {
			t.Fatal(k, seen)
		}
	}
}
