export interface DictTypeQuery extends Api.PageParams {
  code?: string
  name?: string
}

export interface DictTypeForm {
  code: string
  name: string
  status: number
}
