/**
 * 客户端定制类型与别名桥。
 *
 * 响应侧契约类型由 uni_core/tools/apigen 从 internal/model/dto/response 生成,
 * 见同目录 api.generated.d.ts(运行 `pnpm gen:api` 重生成)。
 * 本文件只保留两类内容:
 *  1. 客户端定制件:请求表单/查询合并类型(后端无 1:1 DTO,如 UserForm/UserQuery);
 *  2. 别名桥:将生成类型(`*Resp`)对齐到前端历史命名,禁止再手写响应字段,
 *     唯一事实源是 api.generated.d.ts。
 */
declare namespace Api {
  /** 通用分页请求参数 */
  interface PageParams {
    page?: number
    pageSize?: number
  }

  /** 认证类型 */
  namespace Auth {
    /** 生成契约:LoginResp、UserInfoResp 等见 api.generated.d.ts */
    interface LoginParams {
      username: string
      password: string
      captchaKey?: string
      captchaCode?: string
    }

    /** 验证码响应(后端 internal/pkg/captcha,不在 dto/response 生成范围) */
    interface CaptchaResp {
      captchaKey: string
      captchaImage: string
    }

    /** 修改密码参数(个人中心) */
    interface ChangePasswordParams {
      oldPassword: string
      newPassword: string
    }

    /** 锁屏解锁:验证当前登录密码(后端 POST /user/password/verify) */
    interface VerifyPasswordParams {
      password: string
    }

    /** 更新个人资料参数(个人中心,仅姓名/邮箱/手机号可自助维护) */
    interface UpdateProfileParams {
      realName: string
      email: string
      phone: string
      nickname?: string
      gender?: string
    }

    /** 别名桥 → api.generated.d.ts(响应字段唯一事实源) */
    type TokenResp = LoginResp
    type AvatarResp = AvatarUploadResp
    type UserInfo = UserInfoResp
    type RoleBrief = RoleBriefResp
    type LoginStats = LoginStatsResp
    type DailyActivity = DailyActivityResp
    type RecentLogin = RecentLoginResp
    type UserOverview = UserOverviewResp
  }

  /** 字典 */
  namespace Dict {
    /** 别名桥 → api.generated.d.ts */
    type DictType = DictTypeResp
    type DictData = DictDataResp

    /** 字典类型表单 */
    type DictTypeForm = Pick<Api.Dict.DictType, 'code' | 'name' | 'status' | 'remark'>
    /** 字典数据表单 */
    type DictDataForm = Pick<
      Api.Dict.DictData,
      'label' | 'value' | 'listClass' | 'isDefault' | 'sort' | 'status' | 'remark'
    >
  }

  /** 系统管理:用户 / 角色 / 菜单 / 部门 */
  namespace System {
    /** 别名桥 → api.generated.d.ts */
    type User = UserResp
    type Role = RoleResp
    type RoleOption = RoleOptionResp
    type Menu = MenuResp
    type Dept = DeptResp

    /** 以下为客户端定制件(请求侧合并类型,后端无 1:1 DTO) */
    interface UserQuery extends Api.PageParams {
      username?: string
      realName?: string
      phone?: string
      email?: string
      status?: number
      deptId?: string
      createdAtStart?: string
      createdAtEnd?: string
    }
    interface UserForm {
      id?: string
      username?: string
      password?: string
      realName?: string
      email?: string
      phone?: string
      deptId?: string
      roleIds?: string[]
      status?: number
      remark?: string
      nickname?: string
      gender?: string
    }

    interface RoleQuery extends Api.PageParams {
      name?: string
      code?: string
      status?: number
    }
    interface RoleForm {
      id?: string
      name: string
      code: string
      dataScope?: number
      sort?: number
      status?: number
      remark?: string
      menuIds?: string[]
      deptIds?: string[]
    }

    interface MenuForm {
      id?: string
      // 字符串为已有菜单 ID;数字 0 表示顶级目录(服务端 parent_id=0)。
      parentId?: string | number
      name: string
      type: 'dir' | 'menu' | 'btn'
      perms?: string
      path?: string
      component?: string
      icon?: string
      sort?: number
      visible?: number
      status?: number
    }

    interface DeptForm {
      id?: string
      // 字符串为已有部门 ID;数字 0 表示顶级部门(服务端 parent_id=0)。
      parentId?: string | number
      name: string
      sort?: number
      leader?: string
      phone?: string
      email?: string
      status?: number
    }
  }

  /** 通知公告 */
  namespace Notice {
    /** 别名桥 → api.generated.d.ts */
    type Notice = NoticeResp
    type MyItem = NoticeMyItemResp
    type MyList = NoticeMyListResp
    type ReadUser = NoticeReadUserResp
    type TargetUser = NoticeTargetUserResp

    interface Query extends Api.PageParams {
      title?: string
      noticeType?: number
      status?: number
    }
    interface Form {
      id?: string
      title: string
      content: string
      noticeType?: number
      priority?: number
      publishType?: number
      /** 当前状态(回显用,提交时后端主导) */
      status?: number
      /** 接收范围类型 0=全体成员 1=指定角色 2=指定部门 3=指定个人 */
      targetType?: number
      /** 接收对象 ID 逗号分隔 */
      targetIds?: string
    }
    interface ReadUsersQuery extends Api.PageParams {
      searchValue?: string
    }
  }

  /** 参数配置 */
  namespace Config {
    /** 别名桥 → api.generated.d.ts */
    type Config = ConfigResp

    interface Query extends Api.PageParams {
      configKey?: string
      configType?: string
      status?: number
    }
    interface Form {
      id?: string
      name: string
      configKey?: string
      configValue: string
      configType: 'S' | 'N' | 'B' | 'J'
      remark?: string
      status?: number
    }
  }

  /** 定时任务 */
  namespace Job {
    /** 别名桥 → api.generated.d.ts */
    type Job = JobResp
    type JobLog = JobLogResp
    type TargetInfo = JobTargetResp
    type Health = JobHealthResp

    interface Query extends Api.PageParams {
      name?: string
      jobGroup?: string
      status?: number
    }
    interface LogQuery extends Api.PageParams {
      jobId?: string
      status?: number
      startTime?: string
      endTime?: string
    }
    interface Form {
      id?: string
      name?: string
      jobGroup?: string
      cronExpression?: string
      invokeTarget?: string
      invokeParams?: string
      concurrent?: number
      retryCount?: number
      retryInterval?: number
      status?: number
      runAtStartup?: number
      remark?: string
    }
  }

  /** 日志 */
  namespace Log {
    /** 别名桥 → api.generated.d.ts */
    type OperationLog = OperationLogResp
    type LoginLog = LoginLogResp

    interface OperationLogQuery extends Api.PageParams {
      username?: string
      module?: string
      operationType?: string
      code?: number
      startTime?: string
      endTime?: string
    }
    interface LoginLogQuery extends Api.PageParams {
      username?: string
      ip?: string
      code?: number
      startTime?: string
      endTime?: string
    }
  }

  /** 监控 */
  namespace Monitor {
    /** 别名桥 → api.generated.d.ts(ServerMonitorResp 及各 Info 类型) */
    type ServerStats = ServerMonitorResp
  }

  /** 文件管理 */
  namespace File {
    /** 文件分类(与后端 entity.FileCategory* 常量一致,客户端枚举) */
    type FileCategory = 'image' | 'video' | 'audio' | 'document' | 'archive' | 'code' | 'other'

    /** 别名桥 → api.generated.d.ts */
    type FileItem = FileResp
    type FileStats = FileStatsResp
    type UploadResp = UploadFilesResp

    /** 列表查询参数 */
    interface FileQuery {
      page?: number
      pageSize?: number
      keyword?: string
      category?: FileCategory
      sortBy?: 'createdAt' | 'name' | 'size'
      sortOrder?: 'asc' | 'desc'
    }
  }
}
