<script setup lang="ts">
import { ref, onMounted, computed, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { ScrollArea } from '@/components/ui/scroll-area'
import { Skeleton } from '@/components/ui/skeleton'
import { Badge } from '@/components/ui/badge'
import { Progress } from '@/components/ui/progress'
import {
  usageService,
  type UsageResponse,
  type PhoneUsage,
} from '@/services/api'
import { PageHeader, ErrorState, DateRangePicker } from '@/components/shared'
import { useDateRange } from '@/composables/useDateRange'
import {
  Gauge,
  MessageSquare,
  Wrench,
  Megaphone,
  ShieldCheck,
  HelpCircle,
  Phone,
} from 'lucide-vue-next'

const { t } = useI18n()

const usage = ref<UsageResponse | null>(null)
const isLoading = ref(true)
const error = ref<string | null>(null)

const {
  selectedRange,
  customDateRange,
  isDatePickerOpen,
  dateRange,
  formatDateRangeDisplay,
  applyCustomRange: applyCustomRangeBase,
} = useDateRange({ defaultPreset: 'this_month', storageKey: 'usage_meters' })

const categoryCards = computed(() => {
  const totals = usage.value?.totals
  return [
    {
      key: 'service',
      label: t('usage.service'),
      description: t('usage.serviceDesc'),
      value: totals?.service ?? 0,
      icon: MessageSquare,
      color: 'bg-emerald-500/20',
      iconColor: 'text-emerald-400',
    },
    {
      key: 'utility',
      label: t('usage.utility'),
      description: t('usage.utilityDesc'),
      value: totals?.utility ?? 0,
      icon: Wrench,
      color: 'bg-blue-500/20',
      iconColor: 'text-blue-400',
    },
    {
      key: 'marketing',
      label: t('usage.marketing'),
      description: t('usage.marketingDesc'),
      value: totals?.marketing ?? 0,
      icon: Megaphone,
      color: 'bg-orange-500/20',
      iconColor: 'text-orange-400',
    },
    {
      key: 'authentication',
      label: t('usage.authentication'),
      description: t('usage.authenticationDesc'),
      value: totals?.authentication ?? 0,
      icon: ShieldCheck,
      color: 'bg-purple-500/20',
      iconColor: 'text-purple-400',
    },
  ]
})

const unknownCount = computed(() => usage.value?.totals.unknown ?? 0)
const totalOutbound = computed(() => usage.value?.totals.total_outbound ?? 0)
const phones = computed(() => usage.value?.phones ?? [])
const freeAllowance = computed(() => usage.value?.free_service_allowance ?? 1000)
const periodLabel = computed(() => usage.value?.period.label ?? '')

function allowancePercent(phone: PhoneUsage): number {
  const limit = phone.service_allowance.limit || freeAllowance.value || 1
  const used = phone.service_allowance.used ?? 0
  return Math.min(100, Math.round((used / limit) * 100))
}

function allowanceStatus(phone: PhoneUsage): 'ok' | 'warn' | 'over' {
  const pct = allowancePercent(phone)
  if (pct >= 100) return 'over'
  if (pct >= 80) return 'warn'
  return 'ok'
}

function formatNumber(n: number): string {
  return new Intl.NumberFormat().format(n)
}

const fetchUsage = async () => {
  isLoading.value = true
  error.value = null
  try {
    const { from, to } = dateRange.value
    const response = await usageService.get({ from, to })
    const data = (response.data as any).data || response.data
    usage.value = data as UsageResponse
  } catch (err) {
    console.error('Failed to load usage:', err)
    error.value = t('usage.errorLoading')
    usage.value = null
  } finally {
    isLoading.value = false
  }
}

const applyCustomRange = () => {
  applyCustomRangeBase()
  fetchUsage()
}

watch(selectedRange, (newValue) => {
  if (newValue !== 'custom') {
    fetchUsage()
  }
})

onMounted(() => {
  fetchUsage()
})
</script>

<template>
  <div class="flex flex-col h-full">
    <PageHeader
      :title="$t('usage.title')"
      :description="$t('usage.subtitle')"
      :icon="Gauge"
      icon-gradient="bg-gradient-to-br from-teal-500 to-cyan-600 shadow-teal-500/20"
    >
      <template #actions>
        <div class="flex items-center gap-2">
          <DateRangePicker
            v-model:selected-range="selectedRange"
            v-model:custom-date-range="customDateRange"
            v-model:is-date-picker-open="isDatePickerOpen"
            :format-date-range-display="formatDateRangeDisplay"
            @apply-custom="applyCustomRange"
          />
        </div>
      </template>
    </PageHeader>

    <ScrollArea class="flex-1">
      <div class="p-6 space-y-6">
        <ErrorState
          v-if="error && !isLoading"
          :title="$t('common.loadErrorTitle')"
          :description="error"
          :retry-label="$t('common.retry')"
          @retry="fetchUsage"
        />

        <template v-if="!error">
          <!-- Period + totals summary -->
          <div class="flex flex-wrap items-center gap-2 text-sm text-white/50 light:text-gray-500">
            <template v-if="isLoading">
              <Skeleton class="h-5 w-40 bg-white/[0.08] light:bg-gray-200" />
            </template>
            <template v-else>
              <span>{{ $t('usage.period') }}:</span>
              <Badge variant="secondary">{{ periodLabel || dateRange.from + ' → ' + dateRange.to }}</Badge>
              <span class="mx-1">·</span>
              <span>{{ $t('usage.totalOutbound') }}:</span>
              <span class="font-medium text-white light:text-gray-900">{{ formatNumber(totalOutbound) }}</span>
            </template>
          </div>

          <!-- Category breakdown -->
          <div class="grid gap-4 md:grid-cols-2 lg:grid-cols-4">
            <template v-if="isLoading">
              <div
                v-for="i in 4"
                :key="i"
                class="rounded-xl border border-white/[0.08] bg-white/[0.02] p-6 light:bg-white light:border-gray-200"
              >
                <div class="flex flex-row items-center justify-between space-y-0 pb-2">
                  <Skeleton class="h-4 w-24 bg-white/[0.08] light:bg-gray-200" />
                  <Skeleton class="h-10 w-10 rounded-lg bg-white/[0.08] light:bg-gray-200" />
                </div>
                <div class="pt-2">
                  <Skeleton class="h-8 w-20 mb-2 bg-white/[0.08] light:bg-gray-200" />
                  <Skeleton class="h-3 w-32 bg-white/[0.08] light:bg-gray-200" />
                </div>
              </div>
            </template>
            <template v-else>
              <div
                v-for="card in categoryCards"
                :key="card.key"
                class="card-depth rounded-xl border border-white/[0.08] bg-white/[0.04] p-6 light:bg-white light:border-gray-200"
              >
                <div class="flex flex-row items-center justify-between space-y-0 pb-2">
                  <span class="text-sm font-medium text-white/50 light:text-gray-500">{{ card.label }}</span>
                  <div class="h-10 w-10 rounded-lg flex items-center justify-center" :class="card.color">
                    <component :is="card.icon" class="h-5 w-5" :class="card.iconColor" />
                  </div>
                </div>
                <div class="pt-2">
                  <div class="text-3xl font-bold text-white light:text-gray-900">
                    {{ formatNumber(card.value) }}
                  </div>
                  <p class="text-xs text-white/40 light:text-gray-500 mt-1">{{ card.description }}</p>
                </div>
              </div>
            </template>
          </div>

          <!-- Unknown category note -->
          <div
            v-if="!isLoading && unknownCount > 0"
            class="flex items-center gap-2 rounded-lg border border-amber-500/20 bg-amber-500/10 px-4 py-3 text-sm text-amber-200 light:text-amber-800 light:bg-amber-50 light:border-amber-200"
          >
            <HelpCircle class="h-4 w-4 shrink-0" />
            <span>{{ $t('usage.unknownNote', { count: formatNumber(unknownCount) }) }}</span>
          </div>

          <!-- Per-phone free service allowance -->
          <Card>
            <CardHeader>
              <CardTitle class="flex items-center gap-2">
                <Phone class="h-5 w-5" />
                {{ $t('usage.phoneAllowances') }}
              </CardTitle>
              <CardDescription>
                {{ $t('usage.phoneAllowancesDesc', { limit: formatNumber(freeAllowance) }) }}
              </CardDescription>
            </CardHeader>
            <CardContent>
              <template v-if="isLoading">
                <div class="space-y-4">
                  <div v-for="i in 3" :key="i" class="space-y-2">
                    <Skeleton class="h-4 w-48 bg-white/[0.08] light:bg-gray-200" />
                    <Skeleton class="h-2 w-full bg-white/[0.08] light:bg-gray-200" />
                    <Skeleton class="h-3 w-32 bg-white/[0.08] light:bg-gray-200" />
                  </div>
                </div>
              </template>
              <template v-else-if="phones.length === 0">
                <div class="py-10 text-center text-muted-foreground">
                  <Phone class="h-10 w-10 mx-auto mb-3 opacity-40" />
                  <p class="font-medium">{{ $t('usage.noPhones') }}</p>
                  <p class="text-sm mt-1">{{ $t('usage.noPhonesDesc') }}</p>
                </div>
              </template>
              <template v-else>
                <div class="space-y-6">
                  <div
                    v-for="phone in phones"
                    :key="phone.whatsapp_account + phone.phone_id"
                    class="space-y-2"
                  >
                    <div class="flex flex-wrap items-center justify-between gap-2">
                      <div class="min-w-0">
                        <p class="font-medium text-white light:text-gray-900 truncate">
                          {{ phone.whatsapp_account }}
                        </p>
                        <p v-if="phone.phone_id" class="text-xs text-white/40 light:text-gray-500 truncate">
                          {{ $t('usage.phoneId') }}: {{ phone.phone_id }}
                        </p>
                      </div>
                      <div class="flex items-center gap-2 text-sm">
                        <Badge
                          :variant="allowanceStatus(phone) === 'ok' ? 'secondary' : 'destructive'"
                          v-if="allowanceStatus(phone) !== 'ok'"
                        >
                          {{ allowanceStatus(phone) === 'over' ? $t('usage.allowanceExhausted') : $t('usage.allowanceNearLimit') }}
                        </Badge>
                        <span class="text-white/60 light:text-gray-600 tabular-nums">
                          {{ formatNumber(phone.service_allowance.used) }}
                          /
                          {{ formatNumber(phone.service_allowance.limit) }}
                        </span>
                      </div>
                    </div>
                    <Progress :model-value="allowancePercent(phone)" class="h-2" />
                    <div class="flex flex-wrap gap-x-4 gap-y-1 text-xs text-white/40 light:text-gray-500">
                      <span>{{ $t('usage.remaining') }}: {{ formatNumber(phone.service_allowance.remaining) }}</span>
                      <span>{{ $t('usage.service') }}: {{ formatNumber(phone.counts.service) }}</span>
                      <span>{{ $t('usage.utility') }}: {{ formatNumber(phone.counts.utility) }}</span>
                      <span>{{ $t('usage.marketing') }}: {{ formatNumber(phone.counts.marketing) }}</span>
                      <span>{{ $t('usage.authentication') }}: {{ formatNumber(phone.counts.authentication) }}</span>
                      <span v-if="phone.counts.unknown">{{ $t('usage.unknown') }}: {{ formatNumber(phone.counts.unknown) }}</span>
                      <span>{{ $t('usage.total') }}: {{ formatNumber(phone.counts.total_outbound) }}</span>
                    </div>
                  </div>
                </div>
              </template>
            </CardContent>
          </Card>

          <p class="text-xs text-white/30 light:text-gray-400">
            {{ $t('usage.disclaimer') }}
          </p>
        </template>
      </div>
    </ScrollArea>
  </div>
</template>
