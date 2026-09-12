package response

// DictItem represents a single dictionary data entry in the cache.
type DictItem struct {
	Label     string `json:"label"`
	Value     string `json:"value"`
	ListClass string `json:"list_class"`
	IsDefault bool   `json:"is_default"`
	Sort      int    `json:"sort"`
}
