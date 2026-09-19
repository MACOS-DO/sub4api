export interface AccountTestUpstreamResponse {
  sequence: number
  method: string
  stage: string
  status_code: number
  headers: Record<string, string[]>
}

export function formatUpstreamResponse(response: AccountTestUpstreamResponse): string {
  const headers = Object.entries(response.headers).flatMap(([name, values]) =>
    values.length ? values.map(value => `${name}: ${value}`) : [`${name}:`]
  )
  return [`#${response.sequence} ${response.method} ${response.stage} — ${response.status_code}`, ...headers].join('\n')
}
