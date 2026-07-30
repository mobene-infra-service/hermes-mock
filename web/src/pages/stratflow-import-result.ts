import { SF_IMPORT_RUN_CREATED, type SfImportPlan, type SfImportResult, type SfImportRowError } from '../types'

export function selectImportedRun(result: SfImportResult, preferredDefCode?: string): SfImportPlan | undefined {
  const successful = (result.plans || []).filter((plan) =>
    plan.result === SF_IMPORT_RUN_CREATED && !!plan.runCode)
  return successful.find((plan) => plan.defCode === preferredDefCode) ?? successful[0]
}

export type ImportErrorDetailState = {
  kind: 'none' | 'complete' | 'truncated' | 'not-retained'
  shown: number
  fail: number
}

type ImportErrorSummary = {
  fail: number
  errors: SfImportRowError[]
  errorsTruncated: boolean
}

export function importErrorDetailState(result: ImportErrorSummary): ImportErrorDetailState {
  const shown = result.errors?.length || 0
  if (result.fail <= 0) return { kind: 'none', shown, fail: result.fail }
  if (result.errorsTruncated && shown === 0) return { kind: 'not-retained', shown, fail: result.fail }
  if (result.errorsTruncated) return { kind: 'truncated', shown, fail: result.fail }
  return { kind: 'complete', shown, fail: result.fail }
}
