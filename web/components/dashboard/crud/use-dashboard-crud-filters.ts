"use client"

import { useEffect, useMemo, useState } from "react"

import {
  buildDashboardCrudInitialFilters,
  type DashboardCrudFilterStateConfig,
} from "./dashboard-crud-utils"

export function useDashboardCrudFilters(
  filters: ReadonlyArray<DashboardCrudFilterStateConfig>
) {
  const defaultsKey = filters
    .map((filter) => `${filter.name}:${String(filter.defaultValue)}`)
    .join("|")
  const initialFilters = useMemo(
    () => buildDashboardCrudInitialFilters(filters),
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [defaultsKey]
  )
  const [draftFilters, setDraftFilters] = useState(initialFilters)
  const [appliedFilters, setAppliedFilters] = useState(initialFilters)

  useEffect(() => {
    setDraftFilters(initialFilters)
    setAppliedFilters(initialFilters)
  }, [initialFilters])

  function setDraftFilter(name: string, value: string | number | undefined) {
    setDraftFilters((current) => ({
      ...current,
      [name]: value,
    }))
  }

  function applyFilters() {
    setAppliedFilters(draftFilters)
  }

  function applyFilter(name: string, value: string | number | undefined) {
    setDraftFilters((current) => ({
      ...current,
      [name]: value,
    }))
    setAppliedFilters((current) => ({
      ...current,
      [name]: value,
    }))
  }

  function resetFilters() {
    setDraftFilters(initialFilters)
    setAppliedFilters(initialFilters)
  }

  return {
    draftFilters,
    appliedFilters,
    setDraftFilter,
    setDraftFilters,
    applyFilter,
    applyFilters,
    resetFilters,
  }
}
