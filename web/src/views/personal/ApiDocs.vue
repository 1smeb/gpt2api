<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { ElMessage } from 'element-plus'
import {
  listMyModels,
  listMyUsageLogs,
  listMyImageTasks,
  getMyUsageStats,
  type SimpleModel,
  type UsageItem,
  type ImageTask,
  type MyStatsResp,
} from '@/api/me'
import { formatCredit, formatDateTime, formatErrorCode } from '@/utils/format'
import { ENABLE_CHAT_MODEL } from '@/config/feature'

// 列表预览统一走缩略图代理:体积压到 ~5-10KB,首屏显著加速。
function withThumb(url: string, kb = 10): string {
  if (!url) return url
  const sep = url.includes('?') ? '&' : '?'
  return `${url}${sep}thumb_kb=${kb}`
}

const activeTab = ref<'chat' | 'image'>(ENABLE_CHAT_MODEL ? 'chat' : 'image')

const models = ref<SimpleModel[]>([])
const chatModels = computed(() => models.value.filter((m) => m.type === 'chat'))
const imageModels = computed(() => models.value.filter((m) => m.type === 'image'))

const selectedChatModel = ref<string>('')
const selectedImageModel = ref<string>('')

// 原点:浏览器当前地址,用于 SDK 示例的 base_url
const origin = computed(() => window.location.origin)

// ---------- 当前用户汇总 ----------
const stats = ref<MyStatsResp | null>(null)
const statsLoading = ref(false)

async function loadStats() {
  statsLoading.value = true
  try {
    stats.value = await getMyUsageStats({ days: 14, top_n: 5 })
  } finally {
    statsLoading.value = false
  }
}

// ---------- 文字历史(chat) ----------
const chatLogs = ref<UsageItem[]>([])
const chatPage = ref({ limit: 20, offset: 0, total: 0 })
const chatLoading = ref(false)

async function loadChatLogs() {
  chatLoading.value = true
  try {
    const data = await listMyUsageLogs({
      type: 'chat',
      limit: chatPage.value.limit,
      offset: chatPage.value.offset,
    })
    chatLogs.value = data.items
    chatPage.value.total = data.total
  } finally {
    chatLoading.value = false
  }
}

function chatPageChange(p: number) {
  chatPage.value.offset = (p - 1) * chatPage.value.limit
  loadChatLogs()
}

// ---------- 图片历史 ----------
const imageTasks = ref<ImageTask[]>([])
const imagePage = ref({ limit: 12, offset: 0 })
const imageLoading = ref(false)
const hasMoreImage = ref(false)
const imageFilter = reactive({
  status: '' as '' | 'success' | 'failed' | 'running' | 'queued' | 'dispatched',
  keyword: '',
  range: [] as string[],
})

function imageFilterParams() {
  const p: Record<string, string> = {}
  if (imageFilter.status) p.status = imageFilter.status
  if (imageFilter.keyword) p.keyword = imageFilter.keyword
  if (imageFilter.range && imageFilter.range.length === 2) {
    p.start_at = imageFilter.range[0]
    p.end_at = imageFilter.range[1]
  }
  return p
}

async function loadImageTasks(reset = true) {
  imageLoading.value = true
  try {
    if (reset) {
      imagePage.value.offset = 0
      imageTasks.value = []
    }
    const data = await listMyImageTasks({
      limit: imagePage.value.limit,
      offset: imagePage.value.offset,
      ...imageFilterParams(),
    })
    if (reset) imageTasks.value = data.items
    else imageTasks.value.push(...data.items)
    hasMoreImage.value = data.items.length >= imagePage.value.limit
  } finally {
    imageLoading.value = false
  }
}

function imageLoadMore() {
  imagePage.value.offset += imagePage.value.limit
  loadImageTasks(false)
}

function onImageFilterReset() {
  imageFilter.status = ''
  imageFilter.keyword = ''
  imageFilter.range = []
  loadImageTasks(true)
}

// ---------- 图片放大 + 下载 ----------
const imgPreviewDlg = ref(false)
const imgPreviewTask = ref<ImageTask | null>(null)
const imgPreviewIdx = ref(0)
const imgPreviewUrls = computed<string[]>(() => imgPreviewTask.value?.image_urls || [])
const imgPreviewCurrent = computed<string>(() => imgPreviewUrls.value[imgPreviewIdx.value] || '')

function openImagePreview(t: ImageTask, idx = 0) {
  if (!t.image_urls?.length) return
  imgPreviewTask.value = t
  imgPreviewIdx.value = idx
  imgPreviewDlg.value = true
}

async function downloadImageOne(url: string, taskID: string, idx: number) {
  if (!url) return
  try {
    const r = await fetch(url, { credentials: 'include' })
    if (!r.ok) throw new Error('HTTP ' + r.status)
    const blob = await r.blob()
    const ct = blob.type || 'image/png'
    const ext = ct.includes('jpeg') ? 'jpg' : ct.split('/')[1] || 'png'
    const a = document.createElement('a')
    const u = URL.createObjectURL(blob)
    a.href = u
    a.download = `${taskID}-${idx + 1}.${ext}`
    document.body.appendChild(a)
    a.click()
    document.body.removeChild(a)
    setTimeout(() => URL.revokeObjectURL(u), 60_000)
  } catch (e: any) {
    ElMessage.error('下载失败:' + (e?.message || e))
  }
}

// ---------- SDK 代码示例 ----------
const chatCurl = computed(() => {
  const model = selectedChatModel.value || 'gpt-5'
  return `curl ${origin.value}/v1/chat/completions \\
  -H "Authorization: Bearer \${YOUR_API_KEY}" \\
  -H "Content-Type: application/json" \\
  -d '{
    "model": "${model}",
    "stream": true,
    "messages": [
      {"role": "user", "content": "你好,介绍一下你自己"}
    ]
  }'`
})

const chatPython = computed(() => {
  const model = selectedChatModel.value || 'gpt-5'
  return `from openai import OpenAI

client = OpenAI(
    base_url="${origin.value}/v1",
    api_key="\${YOUR_API_KEY}",
)

resp = client.chat.completions.create(
    model="${model}",
    messages=[{"role": "user", "content": "你好"}],
    stream=True,
)
for chunk in resp:
    print(chunk.choices[0].delta.content or "", end="")`
})

const imageCurl = computed(() => {
  const model = selectedImageModel.value || 'gpt-image-2'
  return `curl ${origin.value}/v1/images/generations \\
  -H "Authorization: Bearer \${YOUR_API_KEY}" \\
  -H "Content-Type: application/json" \\
  -d '{
    "model": "${model}",
    "prompt": "A cute orange cat playing with yarn, studio ghibli style",
    "n": 1,
    "size": "1024x1024"
  }'`
})

// 图生图 curl —— reference_images 接受 URL / data:URL / 纯 base64 三种写法,
// 一次最多 4 张,单张最大 20MB。后端会自动下载/解码并转给上游。
const imageRefCurl = computed(() => {
  const model = selectedImageModel.value || 'gpt-image-2'
  return `curl ${origin.value}/v1/images/generations \\
  -H "Authorization: Bearer \${YOUR_API_KEY}" \\
  -H "Content-Type: application/json" \\
  -d '{
    "model": "${model}",
    "prompt": "Restyle the cat as a watercolor painting, soft pastel palette",
    "n": 1,
    "size": "1024x1024",
    "reference_images": [
      "https://example.com/cat.png",
      "data:image/png;base64,iVBORw0KGgo..."
    ]
  }'`
})

// 纯 requests 版图片生成,无需 OpenAI SDK 依赖,容易嵌进现有脚本。
const imagePythonRequests = computed(() => {
  const model = selectedImageModel.value || 'gpt-image-2'
  return `import requests

API_KEY = "\${YOUR_API_KEY}"
BASE_URL = "${origin.value}/v1"

resp = requests.post(
    f"{BASE_URL}/images/generations",
    headers={
        "Authorization": f"Bearer {API_KEY}",
        "Content-Type": "application/json",
    },
    json={
        "model": "${model}",
        "prompt": "A cute orange cat playing with yarn",
        "n": 1,
        "size": "1024x1024",
    },
    timeout=300,
)
resp.raise_for_status()
data = resp.json()
print(data["data"][0]["url"])`
})

// 图生图 Python 示例:本地文件 → base64 → reference_images。
// 服务端能识别 URL / data:URL / 纯 base64 三种写法,这里用最常见的本地图。
const imagePythonRefRequests = computed(() => {
  const model = selectedImageModel.value || 'gpt-image-2'
  return `import base64, requests

API_KEY = "\${YOUR_API_KEY}"
BASE_URL = "${origin.value}/v1"

# reference_images 单张最大 20MB,最多 4 张;支持 URL / data:URL / 纯 base64
def img_b64(path: str) -> str:
    with open(path, "rb") as f:
        return base64.b64encode(f.read()).decode()

resp = requests.post(
    f"{BASE_URL}/images/generations",
    headers={
        "Authorization": f"Bearer {API_KEY}",
        "Content-Type": "application/json",
    },
    json={
        "model": "${model}",
        "prompt": "Turn the cat into a watercolor painting",
        "n": 1,
        "size": "1024x1024",
        "reference_images": [
            img_b64("cat.png"),
            # 也可以直接传公网 URL:"https://example.com/style.jpg"
        ],
    },
    timeout=300,
)
resp.raise_for_status()
print(resp.json()["data"][0]["url"])`
})

const imagePython = computed(() => {
  const model = selectedImageModel.value || 'gpt-image-2'
  return `from openai import OpenAI

client = OpenAI(
    base_url="${origin.value}/v1",
    api_key="\${YOUR_API_KEY}",
)

resp = client.images.generate(
    model="${model}",
    prompt="A cute orange cat playing with yarn",
    n=1,
    size="1024x1024",
)
print(resp.data[0].url)`
})

async function copy(text: string) {
  try {
    await navigator.clipboard.writeText(text)
    ElMessage.success('已复制到剪贴板')
  } catch {
    ElMessage.error('复制失败,请手动选择文本')
  }
}

// ---------- 状态标签 ----------
function statusTag(s: string): 'success' | 'warning' | 'danger' | 'info' {
  if (s === 'success') return 'success'
  if (s === 'failed') return 'danger'
  if (s === 'running' || s === 'dispatched' || s === 'queued') return 'warning'
  return 'info'
}

// ---------- 初始化 ----------
onMounted(async () => {
  try {
    const m = await listMyModels()
    models.value = ENABLE_CHAT_MODEL
      ? m.items
      : m.items.filter((x) => x.type !== 'chat')
    const firstChat = m.items.find((x) => x.type === 'chat')
    const firstImage = m.items.find((x) => x.type === 'image')
    if (firstChat) selectedChatModel.value = firstChat.slug
    if (firstImage) selectedImageModel.value = firstImage.slug
  } catch {
    // 忽略
  }
  loadStats()
  if (ENABLE_CHAT_MODEL) loadChatLogs()
  loadImageTasks()
})
</script>

<template>
  <div class="page-container">
    <div class="card-block hero">
      <div>
        <h2 class="page-title">接口文档 & 用量</h2>
        <p class="desc">
          <template v-if="ENABLE_CHAT_MODEL">
            外部调用走 <code>/v1/chat/completions</code> 与 <code>/v1/images/generations</code>,
          </template>
          <template v-else>
            外部调用走 <code>/v1/images/generations</code>,
          </template>
          下面给出 curl / Python SDK 代码片段;个人用量与图片任务汇总在这里。若想在浏览器里直接体验,请打开「在线体验」。
        </p>
      </div>
      <div class="hero-stats" v-loading="statsLoading">
        <div class="stat">
          <div class="lbl">14 天请求</div>
          <div class="val">{{ stats?.overall.requests ?? 0 }}</div>
        </div>
        <div v-if="ENABLE_CHAT_MODEL" class="stat">
          <div class="lbl">文字 Token(in/out)</div>
          <div class="val">{{ stats?.overall.input_tokens ?? 0 }} / {{ stats?.overall.output_tokens ?? 0 }}</div>
        </div>
        <div class="stat">
          <div class="lbl">图片张数</div>
          <div class="val">{{ stats?.overall.image_images ?? 0 }}</div>
        </div>
        <div class="stat">
          <div class="lbl">14 天消耗积分</div>
          <div class="val primary">{{ formatCredit(stats?.overall.credit_cost) }}</div>
        </div>
      </div>
    </div>

    <el-tabs v-model="activeTab" class="pg-tabs">
      <!-- ================== 文字对话 ================== -->
      <el-tab-pane v-if="ENABLE_CHAT_MODEL" label="对话生成(文字模型)" name="chat">
        <div class="card-block">
          <div class="row">
            <div class="label">文字模型</div>
            <el-select v-model="selectedChatModel" placeholder="选择模型" style="width: 320px">
              <el-option
                v-for="m in chatModels"
                :key="m.id"
                :label="`${m.slug}${m.description ? ' · ' + m.description : ''}`"
                :value="m.slug"
              />
            </el-select>
            <router-link to="/personal/keys">
              <el-button text type="primary">没有 Key?去「API Keys」创建</el-button>
            </router-link>
          </div>

          <el-tabs type="border-card" class="code-tabs">
            <el-tab-pane label="curl">
              <pre class="code"><code>{{ chatCurl }}</code></pre>
              <el-button size="small" @click="copy(chatCurl)">复制 curl</el-button>
            </el-tab-pane>
            <el-tab-pane label="Python (OpenAI SDK)">
              <pre class="code"><code>{{ chatPython }}</code></pre>
              <el-button size="small" @click="copy(chatPython)">复制 Python</el-button>
            </el-tab-pane>
          </el-tabs>
        </div>

        <div class="card-block">
          <div class="flex-between" style="margin-bottom: 10px">
            <h3 class="section-title">文字调用历史</h3>
            <el-button size="small" @click="loadChatLogs">刷新</el-button>
          </div>
          <el-table v-loading="chatLoading" :data="chatLogs" stripe size="small">
            <el-table-column prop="created_at" label="时间" min-width="160">
              <template #default="{ row }">{{ formatDateTime(row.created_at) }}</template>
            </el-table-column>
            <el-table-column prop="model_slug" label="模型" min-width="140" />
            <el-table-column label="Token (in / out / cache)" min-width="170">
              <template #default="{ row }">
                {{ row.input_tokens }} / {{ row.output_tokens }}
                <span v-if="row.cache_read_tokens" class="mute">/ {{ row.cache_read_tokens }}</span>
              </template>
            </el-table-column>
            <el-table-column label="耗时" width="90">
              <template #default="{ row }">{{ row.duration_ms }} ms</template>
            </el-table-column>
            <el-table-column label="状态" width="90">
              <template #default="{ row }">
                <el-tag :type="statusTag(row.status)" size="small">{{ row.status }}</el-tag>
                <el-tooltip v-if="row.error_code" :content="formatErrorCode(row.error_code) + '(' + row.error_code + ')'">
                  <el-icon style="margin-left:4px"><InfoFilled /></el-icon>
                </el-tooltip>
              </template>
            </el-table-column>
            <el-table-column label="扣费(积分)" width="110">
              <template #default="{ row }">{{ formatCredit(row.credit_cost) }}</template>
            </el-table-column>
          </el-table>
          <div class="pager">
            <el-pagination
              layout="prev, pager, next, total"
              :total="chatPage.total"
              :page-size="chatPage.limit"
              :current-page="Math.floor(chatPage.offset / chatPage.limit) + 1"
              @current-change="chatPageChange"
            />
          </div>
        </div>
      </el-tab-pane>

      <!-- ================== 图片生成 ================== -->
      <el-tab-pane label="图片生成(图片模型)" name="image">
        <div class="card-block">
          <div class="row">
            <div class="label">图片模型</div>
            <el-select v-model="selectedImageModel" placeholder="选择模型" style="width: 320px">
              <el-option
                v-for="m in imageModels"
                :key="m.id"
                :label="`${m.slug}${m.description ? ' · ' + m.description : ''}`"
                :value="m.slug"
              />
            </el-select>
          </div>

          <el-tabs type="border-card" class="code-tabs">
            <el-tab-pane label="curl(文生图)">
              <pre class="code"><code>{{ imageCurl }}</code></pre>
              <el-button size="small" @click="copy(imageCurl)">复制 curl</el-button>
            </el-tab-pane>
            <el-tab-pane label="curl(图生图)">
              <pre class="code"><code>{{ imageRefCurl }}</code></pre>
              <div class="hint">
                reference_images 支持 <code>URL</code> / <code>data:URL</code> / 纯 <code>base64</code>;
                单次最多 4 张,单张最大 20MB。
              </div>
              <el-button size="small" @click="copy(imageRefCurl)">复制 curl</el-button>
            </el-tab-pane>
            <el-tab-pane label="Python (OpenAI SDK)">
              <pre class="code"><code>{{ imagePython }}</code></pre>
              <el-button size="small" @click="copy(imagePython)">复制 Python</el-button>
            </el-tab-pane>
            <el-tab-pane label="Python (requests · 文生图)">
              <pre class="code"><code>{{ imagePythonRequests }}</code></pre>
              <el-button size="small" @click="copy(imagePythonRequests)">复制 Python</el-button>
            </el-tab-pane>
            <el-tab-pane label="Python (requests · 图生图)">
              <pre class="code"><code>{{ imagePythonRefRequests }}</code></pre>
              <div class="hint">
                reference_images 同时支持 <code>URL</code> / <code>data:URL</code> / 纯 <code>base64</code>,
                最多 4 张、单张最大 20MB,服务端会自动下载并解码。
              </div>
              <el-button size="small" @click="copy(imagePythonRefRequests)">复制 Python</el-button>
            </el-tab-pane>
          </el-tabs>
        </div>

        <div class="card-block">
          <div class="flex-between" style="margin-bottom: 10px">
            <h3 class="section-title">图片任务历史</h3>
            <el-button size="small" @click="loadImageTasks(true)">刷新</el-button>
          </div>
          <el-form inline class="flex-wrap-gap" style="margin-bottom:10px" @submit.prevent="loadImageTasks(true)">
            <el-input v-model="imageFilter.keyword" placeholder="提示词关键字" clearable style="width:220px" />
            <el-select v-model="imageFilter.status" placeholder="状态" clearable style="width:130px">
              <el-option label="成功" value="success" />
              <el-option label="失败" value="failed" />
              <el-option label="运行中" value="running" />
              <el-option label="队列中" value="queued" />
            </el-select>
            <el-date-picker
              v-model="imageFilter.range"
              type="datetimerange"
              unlink-panels
              range-separator="~"
              start-placeholder="开始时间"
              end-placeholder="结束时间"
              format="YYYY-MM-DD HH:mm"
              value-format="YYYY-MM-DD HH:mm:ss"
              style="width:340px"
            />
            <el-button type="primary" @click="loadImageTasks(true)">查询</el-button>
            <el-button @click="onImageFilterReset">重置</el-button>
          </el-form>
          <div v-loading="imageLoading">
            <div v-if="imageTasks.length === 0 && !imageLoading" class="empty">
              暂无图片任务,复制上方代码调用一次即可生成记录。
            </div>
            <div class="grid">
              <el-card
                v-for="t in imageTasks"
                :key="t.id"
                shadow="hover"
                class="img-card"
              >
                <div class="thumb" @click="openImagePreview(t, 0)">
                  <img
                    v-if="t.image_urls?.[0]"
                    :src="withThumb(t.image_urls[0])"
                    :alt="t.prompt"
                    loading="lazy"
                  />
                  <div v-else class="thumb-ph">
                    <el-icon :size="32"><PictureRounded /></el-icon>
                    <div class="s">{{ t.status }}</div>
                  </div>
                  <div v-if="t.image_urls && t.image_urls.length > 1" class="thumb-badge">
                    {{ t.image_urls.length }} 张
                  </div>
                </div>
                <div class="meta">
                  <div class="title" :title="t.prompt">{{ t.prompt || '(无 prompt)' }}</div>
                  <div class="sub">
                    <el-tag size="small" :type="statusTag(t.status)">{{ t.status }}</el-tag>
                    <span>{{ t.size }}</span>
                    <span class="mute">n={{ t.n }}</span>
                  </div>
                  <div class="foot">
                    <span class="mute">{{ formatDateTime(t.created_at) }}</span>
                    <span class="credit">{{ formatCredit(t.credit_cost) }} 积分</span>
                  </div>
                  <div class="actions">
                    <el-button
                      v-if="t.image_urls?.length"
                      size="small" type="primary" link
                      @click="openImagePreview(t, 0)"
                    >放大</el-button>
                    <el-button
                      v-if="t.image_urls?.length"
                      size="small" link
                      @click="downloadImageOne(t.image_urls[0], t.task_id, 0)"
                    >下载</el-button>
                  </div>
                  <div v-if="t.error" class="err">{{ t.error }}</div>
                </div>
              </el-card>
            </div>
            <div v-if="hasMoreImage" class="pager">
              <el-button @click="imageLoadMore">加载更多</el-button>
            </div>
          </div>
        </div>
      </el-tab-pane>
    </el-tabs>

    <!-- 图片放大预览(大图主视图 + 缩略图切换) -->
    <el-dialog v-model="imgPreviewDlg" title="图片预览" width="780px">
      <div v-if="imgPreviewTask">
        <div class="prompt-line" :title="imgPreviewTask.prompt">{{ imgPreviewTask.prompt }}</div>
        <div class="big-img-wrap">
          <el-image
            :src="imgPreviewCurrent"
            :preview-src-list="imgPreviewUrls"
            :initial-index="imgPreviewIdx"
            fit="contain"
            style="max-height:60vh;max-width:100%;cursor:zoom-in"
          />
        </div>
        <div v-if="imgPreviewUrls.length > 1" class="thumb-strip">
          <img
            v-for="(u, idx) in imgPreviewUrls"
            :key="idx"
            :src="withThumb(u, 16)"
            alt=""
            loading="lazy"
            :class="['p-thumb', { active: idx === imgPreviewIdx }]"
            @click="imgPreviewIdx = idx"
          />
        </div>
        <div class="dlg-actions">
          <el-button
            size="small" type="primary"
            @click="downloadImageOne(imgPreviewCurrent, imgPreviewTask.task_id, imgPreviewIdx)"
          >下载当前</el-button>
        </div>
      </div>
    </el-dialog>
  </div>
</template>

<style scoped lang="scss">
.page-container { padding: 16px; }
.page-title { margin: 0; font-size: 20px; font-weight: 700; }
.section-title { margin: 0; font-size: 16px; font-weight: 600; }
.card-block {
  background: var(--el-bg-color);
  border: 1px solid var(--el-border-color-lighter);
  border-radius: 8px;
  padding: 16px;
  margin-bottom: 16px;
}
.flex-between { display: flex; justify-content: space-between; align-items: center; }
.hero {
  display: flex; justify-content: space-between; gap: 24px; flex-wrap: wrap;
  .desc { color: var(--el-text-color-secondary); margin-top: 4px; font-size: 13px; }
  code {
    background: var(--el-fill-color-light); padding: 1px 6px; border-radius: 4px; font-size: 12px;
  }
}
.hero-stats {
  display: flex; gap: 24px; flex-wrap: wrap;
  .stat { min-width: 120px; }
  .lbl { font-size: 12px; color: var(--el-text-color-secondary); }
  .val { font-size: 22px; font-weight: 700; margin-top: 2px; }
  .val.primary { color: #409eff; }
}

.pg-tabs { :deep(.el-tabs__header) { margin-bottom: 12px; } }
.row {
  display: flex; gap: 12px; align-items: center; flex-wrap: wrap; margin-bottom: 12px;
  .label { font-weight: 600; min-width: 68px; }
}
.code-tabs {
  :deep(.el-tabs__content) { padding: 12px; }
}
.code {
  background: #1f2937; color: #e5e7eb; border-radius: 6px;
  padding: 12px 14px; margin: 0 0 10px; font-size: 12px; line-height: 1.6;
  overflow-x: auto; white-space: pre-wrap; word-break: break-word;
  font-family: "SFMono-Regular", Consolas, "Liberation Mono", Menlo, monospace;
}
:global(html.dark) .code { background: #0f1115; }

.mute { color: var(--el-text-color-secondary); }
.pager { margin-top: 12px; display: flex; justify-content: flex-end; }
.empty { padding: 24px 0; color: var(--el-text-color-secondary); text-align: center; }

.grid {
  display: grid; grid-template-columns: repeat(auto-fill, minmax(240px, 1fr)); gap: 12px;
}
.img-card {
  :deep(.el-card__body) { padding: 0; }
  .thumb {
    height: 180px; display: flex; align-items: center; justify-content: center;
    background: var(--el-fill-color-lighter);
    img { max-width: 100%; max-height: 100%; object-fit: contain; }
  }
  .thumb-ph { text-align: center; color: var(--el-text-color-secondary); .s { font-size: 12px; } }
  .meta { padding: 10px 12px; }
  .title {
    font-size: 13px; font-weight: 600; margin-bottom: 6px;
    overflow: hidden; white-space: nowrap; text-overflow: ellipsis;
  }
  .sub { display: flex; gap: 6px; font-size: 12px; align-items: center; color: var(--el-text-color-regular); }
  .foot {
    display: flex; justify-content: space-between; margin-top: 6px; font-size: 12px;
    .credit { color: #e6a23c; font-weight: 600; }
  }
  .err {
    color: var(--el-color-danger); font-size: 12px; margin-top: 6px;
    background: var(--el-color-danger-light-9); padding: 4px 6px; border-radius: 4px;
    white-space: pre-wrap; word-break: break-word;
  }
}

.flex-wrap-gap { display: flex; flex-wrap: wrap; gap: 8px; align-items: center; }

.hint {
  font-size: 12px; color: var(--el-text-color-secondary); margin: -4px 0 8px;
  code { background: var(--el-fill-color-light); padding: 1px 4px; border-radius: 3px; }
}

.img-card {
  .thumb {
    position: relative; cursor: zoom-in;
    .thumb-badge {
      position: absolute; top: 6px; right: 6px;
      background: rgba(0, 0, 0, 0.6); color: #fff;
      border-radius: 10px; padding: 2px 8px; font-size: 11px;
    }
  }
  .actions { display: flex; gap: 6px; margin-top: 4px; }
}

.prompt-line {
  font-size: 13px; color: var(--el-text-color-secondary);
  margin-bottom: 10px; word-break: break-all;
}
.big-img-wrap {
  display: flex; justify-content: center; align-items: center;
  background: var(--el-fill-color-darker); border-radius: 6px;
  padding: 8px; min-height: 360px;
}
.thumb-strip {
  display: flex; gap: 6px; margin-top: 10px;
  overflow-x: auto; padding-bottom: 4px;
}
.p-thumb {
  width: 64px; height: 64px; border-radius: 4px;
  object-fit: cover; cursor: pointer;
  border: 2px solid transparent; flex-shrink: 0;
}
.p-thumb.active { border-color: var(--el-color-primary); }
.dlg-actions {
  display: flex; justify-content: flex-end; margin-top: 12px;
}

@media (max-width: 640px) {
  .hero { flex-direction: column; }
  .hero-stats { gap: 16px; }
}
</style>
