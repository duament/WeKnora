# Dolphin 跨工作区控制面 API

[返回目录](./01-api-overview.md)

Dolphin 系列接口是**跨工作区（cross-workspace）控制面 API**，供上游平台/运营方使用：调用方已经理解用户意图后，以目标用户的身份在其名下所有工作区内读取知识库、执行检索。

| 方法 | 路径 | 描述 |
| ---- | ---- | ---- |
| GET | `/dolphin/knowledge-bases` | 列出指定用户的所有知识库（含共享） |
| GET | `/dolphin/users/{email}/knowledge-bases` | 列出指定用户的所有知识库（path 变体） |
| POST | `/dolphin/search-context` | 以指定用户身份搜索知识库并返回处理完成的上下文 |
| POST | `/dolphin/users/{email}/search-context` | 以指定用户身份搜索知识库（path 变体） |

## 认证与权限

- 必须使用**平台 admin API key**（通过 `/system/admin/api-keys` 创建），携带 **`system_tenants_read` 或 `system_tenants_manage`** 能力。
- 请求头传 `X-API-Key: <platform_api_key>`，**不需要** `X-Tenant-ID`（这些接口不依赖调用方自身的工作区上下文）。
- 目标用户通过 `email` 参数指定（body / query / path 三种方式均可），接口以该用户的身份和权限执行操作。
- 工作区（workspace）绑定的普通 API key 会被拒绝，无论其工作区能力如何。

**未知邮箱回退（Fallback）**：

- 若传入的 `email` 在系统中不存在对应用户，且配置了默认组织环境变量 `DOLPHIN_DEFAULT_ORGANIZATION`（默认组织的 UUID），两个接口不会返回 404，而是**按照该默认组织返回数据**：
  - 仅返回**共享进该组织**的知识库（不返回组织成员工作区自建的知识库）；
  - 响应中 `data.user` 的 `id`/`username` 为空（仅有请求的 `email`），并额外返回 `data.organization`（`id`/`name`）标明数据来源；
  - 搜索接口以请求的 email 作为合成身份仅检索该组织的共享知识库。
- 若未配置 `DOLPHIN_DEFAULT_ORGANIZATION`（或组织不存在成员工作区），仍保持原有 404 行为（或返回空结果）。

**错误响应格式**：

```json
{
  "success": false,
  "error": {
    "code": 1003,
    "message": "user not found for email",
    "details": null
  }
}
```

常见错误码：`1000` 参数错误（缺少 email / query 为空）、`1003` 用户不存在、`1007` 服务器内部错误。

---

## GET `/dolphin/knowledge-bases` - 列出指定用户的知识库

以目标用户身份查询其名下所有工作区内**创建的知识库**，以及**共享给其所在工作区**的知识库。

**Query 参数**（与 path 变体二选一）：

| 字段  | 类型   | 必填 | 说明             |
| ----- | ------ | ---- | ---------------- |
| email | string | 是   | 目标用户邮箱（大小写不敏感） |

**GET `/dolphin/users/{email}/knowledge-bases` - path 变体**：`email` 从 URL path 中取，其余行为完全一致。

**请求**:

```curl
# query 方式
curl --location 'http://localhost:8080/api/v1/dolphin/knowledge-bases?email=user%40example.com' \
--header 'X-API-Key: sk-xxxxx'

# path 方式
curl --location 'http://localhost:8080/api/v1/dolphin/users/user%40example.com/knowledge-bases' \
--header 'X-API-Key: sk-xxxxx'
```

**响应**：

```json
{
  "success": true,
  "data": {
    "user": {
      "id": "45800a79-4d15-45cd-bfd4-fa1940e82367",
      "email": "user@example.com",
      "username": "user_name"
    },
    "knowledge_bases": [
      {
        "source": "own",
        "tenant_id": 10008,
        "tenant_name": "user's Workspace",
        "permission": "owner",
        "knowledge_base": {
          "id": "be2ff1ab-b779-4036-826b-d17c8d18b7e2",
          "name": "test",
          "type": "document",
          "knowledge_count": 1,
          "creator_name": "user_name",
          "tenant_id": 10008,
          "vector_store_name": "System default",
          "capabilities": { "keyword": true, "vector": true, "wiki": true, "faq": false, "graph": false },
          "...": "其余为知识库完整字段（分块配置、模型配置、存储配置等）"
        }
      },
      {
        "source": "shared",
        "tenant_id": 10008,
        "tenant_name": "user's Workspace",
        "permission": "editor",
        "knowledge_base": { "id": "d36639a6-409a-434a-bb9a-2fd9092d9912", "name": "DEMO 的问答知识库", "...": "..." }
      }
    ],
    "total": 2
  }
}
```

**返回字段说明**：

| 字段 | 类型 | 说明 |
| ---- | ---- | ---- |
| data.user | object | 目标用户信息（id / email / username） |
| data.knowledge_bases[].source | string | `own`：用户创建；`shared`：共享进其工作区 |
| data.knowledge_bases[].tenant_id / tenant_name | - | 该知识库所属工作区 |
| data.knowledge_bases[].permission | string | 用户在该工作区内的角色（owner / admin / editor / viewer） |
| data.knowledge_bases[].knowledge_base | object | 知识库完整对象 |
| data.total | number | 知识库数量 |

---

## POST `/dolphin/search-context` - 以指定用户身份搜索知识库

调用方已经理解用户意图后调用本接口：以目标用户身份在其所属各工作区内按与公开知识检索 API 相同的路径（检索 → 重排 → 合并 → top K）搜索知识库，返回可直接注入提示词的 RAG 上下文。**不进行查询改写或 LLM 总结**。

**请求体**（body 中的 email 与 path 变体二选一）：

| 字段                | 类型     | 必填 | 说明                                                                 |
| ------------------- | -------- | ---- | -------------------------------------------------------------------- |
| email               | string   | 是*  | 目标用户邮箱（body 传或走 path 变体 `/users/{email}/search-context`） |
| query               | string   | 是   | 已经理解后的搜索查询，不能为空                                       |
| knowledge_base_ids  | string[] | 否   | 限定搜索的知识库 ID 列表；不传则搜索用户所有可访问知识库             |
| knowledge_ids       | string[] | 否   | 进一步限定到指定知识（文件）                                         |
| top_k               | number   | 否   | 返回上下文块数上限；0/不传用租户 rerank_top_k                         |
| rerank_model_id     | string   | 否   | 覆盖租户重排模型                                                     |
| vector_threshold    | number   | 否   | 覆盖向量检索阈值                                                     |
| keyword_threshold   | number   | 否   | 覆盖关键词检索阈值                                                   |
| rerank_threshold    | number   | 否   | 覆盖重排阈值                                                         |

> `knowledge_base_ids` / `knowledge_ids` 支持传空数组或 `null`，等价于"不限定"，将搜索用户所有可访问知识库；列表中的空字符串条目会被自动忽略。

**POST `/dolphin/users/{email}/search-context` - path 变体**：`email` 从 URL path 中取，其余行为完全一致。

**请求**:

```curl
# body 方式：不限定范围，搜索用户所有可访问知识库
curl --location 'http://localhost:8080/api/v1/dolphin/search-context' \
--header 'X-API-Key: sk-xxxxx' \
--header 'Content-Type: application/json' \
--data '{
    "email": "user@example.com",
    "query": "如何部署 CICD 流水线",
    "top_k": 5
}'

# path 方式：限定到指定知识库
curl --location 'http://localhost:8080/api/v1/dolphin/users/user%40example.com/search-context' \
--header 'X-API-Key: sk-xxxxx' \
--header 'Content-Type: application/json' \
--data '{
    "query": "员工手册",
    "knowledge_base_ids": ["be2ff1ab-b779-4036-826b-d17c8d18b7e2"],
    "top_k": 3
}'
```

**响应**：

```json
{
  "success": true,
  "data": {
    "user": {
      "id": "45800a79-4d15-45cd-bfd4-fa1940e82367",
      "email": "user@example.com",
      "username": "user_name"
    },
    "query": "如何部署 CICD 流水线",
    "contexts": "<documents>\n<document>\n<title>71-DEV-CICDforGitLabwithlocalmirrorImages.pdf</title>\n<description>本文档简要说明了内部GitLab使用本地镜像仓库配置Docker Runner的方式...</description>\n</document>\n<context id=\"1\">...</context>\n</documents>",
    "search_results": [
      {
        "id": "2b219883-3151-4a83-8be7-0006209ce6e0",
        "content": "8\n#   CICD for GitLab with local mirror Images\n...",
        "knowledge_id": "7c81b3cf-f3c2-4fa9-8101-ac35a9bd5359",
        "knowledge_base_id": "d2bc917c-d7da-4760-9c75-b86676a7ea4f",
        "knowledge_title": "71-DEV-CICDforGitLabwithlocalmirrorImages.pdf",
        "chunk_index": 0,
        "start_at": 0,
        "end_at": 273,
        "seq": 0,
        "score": 0.5850432141692656,
        "chunk_type": "text",
        "match_type": 0,
        "metadata": { "base_score": "0.0086", "content": "..." }
      }
    ],
    "tenant_results": [
      { "tenant_id": 10008, "tenant_name": "user's Workspace", "result_count": 5 }
    ],
    "total": 5
  }
}
```

**返回字段说明**：

| 字段 | 类型 | 说明 |
| ---- | ---- | ---- |
| data.user | object | 目标用户信息 |
| data.query | string | 回显的查询 |
| data.contexts | string | 处理完成的 RAG 上下文块（`<documents>` + `<context>`），可直接注入提示词 |
| data.search_results[] | array | 合并、去重、按全局相关性排序后的检索结果（chunk 粒度） |
| data.search_results[].id | string | chunk ID |
| data.search_results[].content | string | chunk 内容 |
| data.search_results[].score | number | 相关性得分（越高越相关） |
| data.tenant_results[] | array | 每个工作区的检索结果统计 |
| data.total | number | 返回的检索结果总数 |

> 当搜索结果为空时，`search_results` 与 `tenant_results` 返回 `[]`（而非 `null`），`contexts` 为空字符串。

**错误场景**：

| 场景 | HTTP | error.code | error.message |
| ---- | ---- | ---------- | ------------- |
| 缺少 email | 400 | 1000 | `email is required (body or path)` |
| query 为空 | 400 | 1000 | `query cannot be empty` |
| 用户不存在（且未配置 `DOLPHIN_DEFAULT_ORGANIZATION`） | 404 | 1003 | `user not found for email` |
| 服务器错误 | 500 | 1007 | `failed to search knowledge bases` |

> **访问控制**：即使调用方显式传入某个知识库 ID，若该用户在其所属工作区内没有该库的访问权限（非自有、也未共享），该库会被自动过滤，不返回任何内容。
