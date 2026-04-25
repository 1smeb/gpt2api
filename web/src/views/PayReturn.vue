<script setup lang="ts">
import { computed } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useUserStore } from '@/stores/user'

const route = useRoute()
const router = useRouter()
const userStore = useUserStore()

const status = computed(() => String(route.query.status || 'pending'))
const orderNo = computed(() => String(route.query.out_trade_no || ''))
const tradeNo = computed(() => String(route.query.trade_no || ''))
const message = computed(() => String(route.query.message || '支付结果待确认,请稍后刷新订单'))

const resultIcon = computed(() => {
  if (status.value === 'paid') return 'success'
  if (status.value === 'failed') return 'error'
  return 'warning'
})
const title = computed(() => {
  if (status.value === 'paid') return '支付成功'
  if (status.value === 'failed') return '支付结果异常'
  return '支付待确认'
})

function goBilling() {
  if (userStore.isLoggedIn) {
    router.replace('/personal/billing')
    return
  }
  router.replace({ path: '/login', query: { redirect: '/personal/billing' } })
}
</script>

<template>
  <div class="pay-return-page">
    <section class="result-panel">
      <el-result :icon="resultIcon" :title="title" :sub-title="message">
        <template #extra>
          <div class="actions">
            <el-button type="primary" @click="goBilling">查看账单</el-button>
            <el-button @click="router.replace('/')">返回首页</el-button>
          </div>
        </template>
      </el-result>

      <div v-if="orderNo || tradeNo" class="meta">
        <div v-if="orderNo" class="meta-row">
          <span>本地订单号</span>
          <code>{{ orderNo }}</code>
        </div>
        <div v-if="tradeNo" class="meta-row">
          <span>平台交易号</span>
          <code>{{ tradeNo }}</code>
        </div>
      </div>
    </section>
  </div>
</template>

<style scoped>
.pay-return-page {
  min-height: calc(100vh - 40px);
  display: flex;
  align-items: center;
  justify-content: center;
  padding: 32px 16px;
  box-sizing: border-box;
  background:
    linear-gradient(180deg, rgba(64, 158, 255, 0.08), rgba(103, 194, 58, 0.06)),
    var(--el-bg-color);
}
.result-panel {
  width: min(560px, 100%);
  background: var(--el-bg-color-overlay);
  border: 1px solid var(--el-border-color-lighter);
  border-radius: 8px;
  box-shadow: var(--el-box-shadow-light);
  padding: 8px 18px 22px;
  box-sizing: border-box;
}
.actions {
  display: flex;
  justify-content: center;
  gap: 10px;
  flex-wrap: wrap;
}
.meta {
  border-top: 1px solid var(--el-border-color-lighter);
  padding-top: 14px;
}
.meta-row {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  min-height: 32px;
  color: var(--el-text-color-secondary);
  font-size: 13px;
}
code {
  max-width: 100%;
  overflow-wrap: anywhere;
  background: var(--el-fill-color-light);
  color: var(--el-text-color-primary);
  padding: 3px 7px;
  border-radius: 4px;
  font-size: 12px;
}
@media (max-width: 520px) {
  .result-panel {
    padding: 4px 12px 18px;
  }
  .meta-row {
    align-items: flex-start;
    flex-direction: column;
    gap: 4px;
  }
}
</style>
