import type { ImCustomerSession } from "@/lib/api/im"

export type CustomerSessionScope = Readonly<{
  epoch: number
  channelId: string
  identityKey: string
  customerId: number
}>

export type CustomerSessionAttempt = Readonly<{
  generation: number
  requestKey: string
}>

let epoch = 0
let generation = 0
let activeScope: CustomerSessionScope | null = null
let currentAttempt: CustomerSessionAttempt | null = null

export function beginCustomerSessionAttempt(requestKey: string): CustomerSessionAttempt {
  currentAttempt = Object.freeze({ generation: ++generation, requestKey })
  return currentAttempt
}

export function isCustomerSessionAttemptCurrent(attempt: CustomerSessionAttempt): boolean {
  return currentAttempt !== null &&
    attempt.generation === currentAttempt.generation &&
    attempt.requestKey === currentAttempt.requestKey
}

export function invalidateCustomerSessionScope(): void {
  epoch += 1
  activeScope = null
}

export function activateCustomerSessionScope(session: ImCustomerSession): CustomerSessionScope {
  if (activeScope && (
    activeScope.customerId !== session.customer.id ||
    activeScope.channelId !== session.channelId
  )) {
    epoch += 1
  }
  activeScope = Object.freeze({
    epoch,
    channelId: session.channelId,
    identityKey: session.identityKey,
    customerId: session.customer.id,
  })
  return activeScope
}

export function captureCustomerSessionScope(): CustomerSessionScope | null {
  return activeScope
}

export function isCustomerSessionScopeCurrent(scope: CustomerSessionScope | null): boolean {
  return scope !== null && activeScope !== null &&
    scope.epoch === activeScope.epoch &&
    scope.customerId === activeScope.customerId &&
    scope.channelId === activeScope.channelId
}
