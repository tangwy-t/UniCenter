package app

// PageRequest holds pagination query parameters from the client.
//
// Page is capped (in addition to PageSize) so a hostile page number cannot
// generate an astronomical SQL OFFSET (page=1e9 would otherwise force a deep
// scan even with pageSize capped at 100).
type PageRequest struct {
	Page     int `form:"page" binding:"omitempty,min=1,max=10000"`
	PageSize int `form:"pageSize" binding:"omitempty,min=1,max=100"`
}

// PageResponse is the standard paginated list response envelope.
type PageResponse struct {
	List     any   `json:"list"`
	Total    int64 `json:"total"`
	Page     int   `json:"page"`
	PageSize int   `json:"pageSize"`
}

// GetPage returns the current page number, defaulting to 1 if unset.
func (p *PageRequest) GetPage() int {
	if p.Page <= 0 {
		return 1
	}
	return p.Page
}

// GetPageSize returns the page size, defaulting to 10 if unset.
func (p *PageRequest) GetPageSize() int {
	if p.PageSize <= 0 {
		return 10
	}
	return p.PageSize
}

// Offset calculates the SQL OFFSET value from the current page and page size.
func (p *PageRequest) Offset() int {
	return (p.GetPage() - 1) * p.GetPageSize()
}

// NewPageResponse creates a PageResponse with the given list, total count, and pagination info.
func NewPageResponse(list any, total int64, page, pageSize int) *PageResponse {
	return &PageResponse{List: list, Total: total, Page: page, PageSize: pageSize}
}
