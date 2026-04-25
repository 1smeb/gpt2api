<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { ElMessage } from 'element-plus'
import { Check, DocumentCopy, Refresh, WarningFilled } from '@element-plus/icons-vue'
import * as paymentApi from '@/api/payment'

const loading = ref(false)
const saving = ref(false)
const config = ref<paymentApi.EPayConfig | null>(null)

const form = reactive({
  gateway_url: '',
  pid: '',
  key: '',
  notify_url: '',
  return_url: '',
  sign_type: 'MD5',
  recharge_enabled: true,
})

const statusType = computed(() => config.value?.channel_ready ? 'success' : 'warning')
const statusLabel = computed(() => config.value?.channel_ready ? '通道已就绪' : '通道未就绪')
const keyLabel = computed(() => {
  if (!config.value?.key_set) return '未配置'
  return config.value.key_mask || '已配置'
})
const displayNotifyURL = computed(() => form.notify_url || config.value?.effective_notify_url || '')
const displayReturnURL = computed(() => form.return_url || config.value?.effective_return_url || '')

function fillForm(d: paymentApi.EPayConfig) {
  config.value = d
  form.gateway_url = d.gateway_url || ''
  form.pid = d.pid || ''
  form.key = ''
  form.notify_url = d.notify_url || ''
  form.return_url = d.return_url || ''
  form.sign_type = d.sign_type || 'MD5'
  form.recharge_enabled = d.recharge_enabled
}

async function load() {
  loading.value = true
  try {
    fillForm(await paymentApi.getEPayConfig())
  } finally {
    loading.value = false
  }
}

function validateURL(label: string, value: string) {
  if (!value) return true
  try {
    const u = new URL(value)
    if (u.protocol !== 'http:' && u.protocol !== 'https:') throw new Error('bad scheme')
    if (!u.host) throw new Error('missing host')
    return true
  } catch {
    ElMessage.warning(`${label} 必须是完整的 http/https URL`)
    return false
  }
}

async function save() {
  if (!form.gateway_url.trim()) {
    ElMessage.warning('请输入易支付网关地址')
    return
  }
  if (!form.pid.trim()) {
    ElMessage.warning('请输入商户 ID')
    return
  }
  if (!config.value?.key_set && !form.key.trim()) {
    ElMessage.warning('请输入商户密钥')
    return
  }
  if (!validateURL('网关地址', form.gateway_url)) return
  if (!validateURL('异步通知地址', form.notify_url)) return
  if (!validateURL('同步跳转地址', form.return_url)) return

  saving.value = true
  try {
    const updated = await paymentApi.updateEPayConfig({
      gateway_url: form.gateway_url.trim(),
      pid: form.pid.trim(),
      key: form.key.trim() || undefined,
      notify_url: form.notify_url.trim(),
      return_url: form.return_url.trim(),
      sign_type: form.sign_type || 'MD5',
      recharge_enabled: form.recharge_enabled,
    })
    fillForm(updated)
    ElMessage.success('支付配置已保存')
  } finally {
    saving.value = false
  }
}

async function copy(value: string) {
  if (!value) return
  await navigator.clipboard.writeText(value)
  ElMessage.success('已复制')
}

onMounted(load)
</script>

<template>
  <div class="page-container">
    <div class="card-block payment-page" v-loading="loading">
      <div class="flex-between page-head">
        <div>
          <div class="page-title" style="margin:0">支付管理</div>
          <div class="sub">配置 Z-Pay / 易支付页面跳转接口,保存后新订单立即生效。</div>
        </div>
        <div class="flex-wrap-gap">
          <el-button :icon="Refresh" @click="load">刷新</el-button>
          <el-button type="primary" :icon="Check" :loading="saving" @click="save">保存配置</el-button>
        </div>
      </div>

      <div class="status-grid">
        <div class="status-item">
          <span>支付通道</span>
          <el-tag :type="statusType" size="small">{{ statusLabel }}</el-tag>
        </div>
        <div class="status-item">
          <span>充值入口</span>
          <el-switch v-model="form.recharge_enabled" inline-prompt active-text="启用" inactive-text="关闭" />
        </div>
        <div class="status-item">
          <span>商户密钥</span>
          <code>{{ keyLabel }}</code>
        </div>
      </div>

      <el-alert
        v-if="!config?.channel_ready"
        type="warning"
        :closable="false"
        show-icon
        class="ready-alert"
      >
        <template #title>
          <span>至少需要配置网关地址、商户 ID、商户密钥,并保证回调地址可公网访问。</span>
        </template>
      </el-alert>

      <el-form label-width="130px" label-position="right" class="payment-form">
        <el-form-item label="网关地址" required>
          <el-input v-model="form.gateway_url" placeholder="https://pay.example.com/submit.php" clearable />
        </el-form-item>
        <el-form-item label="商户 ID" required>
          <el-input v-model="form.pid" placeholder="Z-Pay 商户 PID" clearable style="max-width:420px" />
        </el-form-item>
        <el-form-item label="商户密钥" :required="!config?.key_set">
          <el-input
            v-model="form.key"
            type="password"
            show-password
            :placeholder="config?.key_set ? '留空表示不修改当前密钥' : '请输入商户密钥'"
            clearable
            style="max-width:420px"
          />
          <div class="hint">
            当前状态: {{ keyLabel }}
          </div>
        </el-form-item>
        <el-form-item label="签名类型">
          <el-select v-model="form.sign_type" style="width:160px">
            <el-option label="MD5" value="MD5" />
          </el-select>
        </el-form-item>
        <el-form-item label="异步通知地址">
          <el-input v-model="form.notify_url" placeholder="留空自动生成" clearable>
            <template #append>
              <el-button :icon="DocumentCopy" @click="copy(displayNotifyURL)" />
            </template>
          </el-input>
          <div class="hint">当前生效: <code>{{ displayNotifyURL || '未生成' }}</code></div>
        </el-form-item>
        <el-form-item label="同步跳转地址">
          <el-input v-model="form.return_url" placeholder="留空自动生成" clearable>
            <template #append>
              <el-button :icon="DocumentCopy" @click="copy(displayReturnURL)" />
            </template>
          </el-input>
          <div class="hint">当前生效: <code>{{ displayReturnURL || '未生成' }}</code></div>
        </el-form-item>
      </el-form>

      <div class="notice">
        <el-icon><WarningFilled /></el-icon>
        <span>同步跳转只负责用户浏览器回到站点;最终到账仍以已验签的支付结果和订单金额校验为准。</span>
      </div>
    </div>
  </div>
</template>

<style scoped lang="scss">
.payment-page {
  max-width: 980px;
}
.page-head {
  align-items: flex-start;
  gap: 16px;
  margin-bottom: 18px;
}
.sub {
  color: var(--el-text-color-secondary);
  font-size: 13px;
  margin-top: 6px;
}
.status-grid {
  display: grid;
  grid-template-columns: repeat(3, minmax(0, 1fr));
  gap: 12px;
  margin-bottom: 16px;
}
.status-item {
  min-height: 52px;
  border: 1px solid var(--el-border-color-lighter);
  border-radius: 8px;
  padding: 10px 12px;
  box-sizing: border-box;
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 10px;
  color: var(--el-text-color-secondary);
  font-size: 13px;
  background: var(--el-fill-color-blank);
}
.ready-alert {
  margin-bottom: 18px;
}
.payment-form {
  max-width: 820px;
}
.hint {
  width: 100%;
  color: var(--el-text-color-secondary);
  font-size: 12px;
  margin-top: 6px;
  line-height: 1.6;
}
code {
  max-width: 100%;
  overflow-wrap: anywhere;
  background: var(--el-fill-color-light);
  color: var(--el-text-color-primary);
  border-radius: 4px;
  padding: 2px 6px;
  font-size: 12px;
}
.notice {
  display: flex;
  align-items: flex-start;
  gap: 8px;
  color: var(--el-text-color-secondary);
  font-size: 12px;
  line-height: 1.6;
  border-top: 1px solid var(--el-border-color-lighter);
  padding-top: 14px;
  margin-top: 10px;
}
@media (max-width: 760px) {
  .status-grid {
    grid-template-columns: 1fr;
  }
  .payment-form {
    :deep(.el-form-item) {
      display: block;
    }
    :deep(.el-form-item__label) {
      justify-content: flex-start;
      width: auto !important;
      margin-bottom: 6px;
    }
    :deep(.el-form-item__content) {
      margin-left: 0 !important;
    }
  }
}
</style>
