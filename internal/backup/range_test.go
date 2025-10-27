package backup

import (
	"testing"
	"time"
)

func TestParseNumericRange(t *testing.T) {
	bm := &BackupManager{}

	tests := []struct {
		name      string
		input     string
		wantStart int
		wantEnd   int
		wantErr   bool
	}{
		{
			name:      "valid range",
			input:     "1-10",
			wantStart: 1,
			wantEnd:   10,
			wantErr:   false,
		},
		{
			name:      "single item range",
			input:     "5-5",
			wantStart: 5,
			wantEnd:   5,
			wantErr:   false,
		},
		{
			name:      "with spaces",
			input:     " 3 - 7 ",
			wantStart: 3,
			wantEnd:   7,
			wantErr:   false,
		},
		{
			name:    "invalid format",
			input:   "1-10-15",
			wantErr: true,
		},
		{
			name:    "start less than 1",
			input:   "0-5",
			wantErr: true,
		},
		{
			name:    "end before start",
			input:   "10-5",
			wantErr: true,
		},
		{
			name:    "non-numeric",
			input:   "a-b",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			start, end, err := bm.ParseNumericRange(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("ParseNumericRange() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr {
				if start != tt.wantStart {
					t.Errorf("ParseNumericRange() start = %v, want %v", start, tt.wantStart)
				}
				if end != tt.wantEnd {
					t.Errorf("ParseNumericRange() end = %v, want %v", end, tt.wantEnd)
				}
			}
		})
	}
}

func TestSelectObjectsByNumericRange(t *testing.T) {
	bm := &BackupManager{}

	// Create test objects with different timestamps
	baseTime := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	objs := []ObjectInfo{
		{Key: "obj1", LastModified: baseTime.Add(5 * time.Hour)}, // Most recent
		{Key: "obj2", LastModified: baseTime.Add(4 * time.Hour)}, // 2nd
		{Key: "obj3", LastModified: baseTime.Add(3 * time.Hour)}, // 3rd
		{Key: "obj4", LastModified: baseTime.Add(2 * time.Hour)}, // 4th
		{Key: "obj5", LastModified: baseTime.Add(1 * time.Hour)}, // 5th
		{Key: "obj6", LastModified: baseTime},                    // Oldest
	}

	tests := []struct {
		name     string
		start    int
		end      int
		wantKeys []string
		wantErr  bool
	}{
		{
			name:     "first two",
			start:    1,
			end:      2,
			wantKeys: []string{"obj1", "obj2"},
		},
		{
			name:     "middle range",
			start:    3,
			end:      4,
			wantKeys: []string{"obj3", "obj4"},
		},
		{
			name:     "single item",
			start:    1,
			end:      1,
			wantKeys: []string{"obj1"},
		},
		{
			name:     "all items",
			start:    1,
			end:      6,
			wantKeys: []string{"obj1", "obj2", "obj3", "obj4", "obj5", "obj6"},
		},
		{
			name:     "range exceeds available (should cap)",
			start:    5,
			end:      100,
			wantKeys: []string{"obj5", "obj6"},
		},
		{
			name:    "start exceeds available",
			start:   10,
			end:     15,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			selected, err := bm.SelectObjectsByNumericRange(objs, tt.start, tt.end)
			if (err != nil) != tt.wantErr {
				t.Errorf("SelectObjectsByNumericRange() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr {
				if len(selected) != len(tt.wantKeys) {
					t.Errorf("SelectObjectsByNumericRange() returned %d objects, want %d", len(selected), len(tt.wantKeys))
					return
				}
				for i, obj := range selected {
					if obj.Key != tt.wantKeys[i] {
						t.Errorf("SelectObjectsByNumericRange() object[%d].Key = %v, want %v", i, obj.Key, tt.wantKeys[i])
					}
				}
			}
		})
	}
}

func TestParseDateRange(t *testing.T) {
	bm := &BackupManager{}

	tests := []struct {
		name      string
		input     string
		wantStart string // RFC3339 format for comparison
		wantEnd   string
		wantErr   bool
	}{
		{
			name:      "date only range",
			input:     "20240101-20240131",
			wantStart: "2024-01-01T00:00:00Z",
			wantEnd:   "2024-01-31T23:59:59Z",
			wantErr:   false,
		},
		{
			name:      "datetime range",
			input:     "20240101:120000-20240131:180000",
			wantStart: "2024-01-01T12:00:00Z",
			wantEnd:   "2024-01-31T18:00:00Z",
			wantErr:   false,
		},
		{
			name:      "single day",
			input:     "20240115-20240115",
			wantStart: "2024-01-15T00:00:00Z",
			wantEnd:   "2024-01-15T23:59:59Z",
			wantErr:   false,
		},
		{
			name:    "invalid format",
			input:   "20240101",
			wantErr: true,
		},
		{
			name:    "end before start",
			input:   "20240131-20240101",
			wantErr: true,
		},
		{
			name:    "invalid date",
			input:   "20241301-20241331",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			start, end, err := bm.ParseDateRange(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("ParseDateRange() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr {
				wantStart, _ := time.Parse(time.RFC3339, tt.wantStart)
				wantEnd, _ := time.Parse(time.RFC3339, tt.wantEnd)

				if !start.Equal(wantStart) {
					t.Errorf("ParseDateRange() start = %v, want %v", start, wantStart)
				}
				if !end.Equal(wantEnd) {
					t.Errorf("ParseDateRange() end = %v, want %v", end, wantEnd)
				}
			}
		})
	}
}

func TestFilterObjectsByDateRange(t *testing.T) {
	bm := &BackupManager{}

	baseTime := time.Date(2024, 1, 15, 12, 0, 0, 0, time.UTC)
	objs := []ObjectInfo{
		{Key: "obj1", LastModified: baseTime.Add(-10 * 24 * time.Hour)}, // Jan 5
		{Key: "obj2", LastModified: baseTime.Add(-5 * 24 * time.Hour)},  // Jan 10
		{Key: "obj3", LastModified: baseTime},                           // Jan 15
		{Key: "obj4", LastModified: baseTime.Add(5 * 24 * time.Hour)},   // Jan 20
		{Key: "obj5", LastModified: baseTime.Add(10 * 24 * time.Hour)},  // Jan 25
	}

	tests := []struct {
		name     string
		start    time.Time
		end      time.Time
		wantKeys []string
	}{
		{
			name:     "all in range",
			start:    baseTime.Add(-15 * 24 * time.Hour),
			end:      baseTime.Add(15 * 24 * time.Hour),
			wantKeys: []string{"obj1", "obj2", "obj3", "obj4", "obj5"},
		},
		{
			name:     "middle range",
			start:    baseTime.Add(-7 * 24 * time.Hour),
			end:      baseTime.Add(7 * 24 * time.Hour),
			wantKeys: []string{"obj2", "obj3", "obj4"},
		},
		{
			name:     "exact match",
			start:    baseTime,
			end:      baseTime,
			wantKeys: []string{"obj3"},
		},
		{
			name:     "none in range",
			start:    baseTime.Add(20 * 24 * time.Hour),
			end:      baseTime.Add(25 * 24 * time.Hour),
			wantKeys: []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			filtered := bm.FilterObjectsByDateRange(objs, tt.start, tt.end)
			if len(filtered) != len(tt.wantKeys) {
				t.Errorf("FilterObjectsByDateRange() returned %d objects, want %d", len(filtered), len(tt.wantKeys))
				return
			}
			for i, obj := range filtered {
				if obj.Key != tt.wantKeys[i] {
					t.Errorf("FilterObjectsByDateRange() object[%d].Key = %v, want %v", i, obj.Key, tt.wantKeys[i])
				}
			}
		})
	}
}

func TestSelectObjectsForOverwrite(t *testing.T) {
	bm := &BackupManager{}

	// Create test objects with different timestamps
	baseTime := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	objs := []ObjectInfo{
		{Key: "obj1", LastModified: baseTime.Add(5 * time.Hour)}, // Most recent
		{Key: "obj2", LastModified: baseTime.Add(4 * time.Hour)}, // 2nd
		{Key: "obj3", LastModified: baseTime.Add(3 * time.Hour)}, // 3rd
		{Key: "obj4", LastModified: baseTime.Add(2 * time.Hour)}, // 4th
		{Key: "obj5", LastModified: baseTime.Add(1 * time.Hour)}, // 5th
		{Key: "obj6", LastModified: baseTime},                    // Oldest
	}

	tests := []struct {
		name      string
		remainder int
		wantKeys  []string
	}{
		{
			name:      "keep 5, delete 1",
			remainder: 5,
			wantKeys:  []string{"obj6"},
		},
		{
			name:      "keep 3, delete 3",
			remainder: 3,
			wantKeys:  []string{"obj4", "obj5", "obj6"},
		},
		{
			name:      "keep 0, delete all",
			remainder: 0,
			wantKeys:  []string{"obj1", "obj2", "obj3", "obj4", "obj5", "obj6"},
		},
		{
			name:      "keep 1, delete 5",
			remainder: 1,
			wantKeys:  []string{"obj2", "obj3", "obj4", "obj5", "obj6"},
		},
		{
			name:      "keep all (remainder equals total)",
			remainder: 6,
			wantKeys:  []string{},
		},
		{
			name:      "keep all (remainder exceeds total)",
			remainder: 10,
			wantKeys:  []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			selected := bm.SelectObjectsForOverwrite(objs, tt.remainder)
			if len(selected) != len(tt.wantKeys) {
				t.Errorf("SelectObjectsForOverwrite() returned %d objects, want %d", len(selected), len(tt.wantKeys))
				return
			}
			for i, obj := range selected {
				if obj.Key != tt.wantKeys[i] {
					t.Errorf("SelectObjectsForOverwrite() object[%d].Key = %v, want %v", i, obj.Key, tt.wantKeys[i])
				}
			}
		})
	}
}
