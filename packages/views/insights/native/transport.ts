type Fetcher=(route:RequestInfo|URL,init?:RequestInit)=>Promise<Response>
let transport:Fetcher|undefined
export function setInsightsTransport(next:Fetcher){transport=next}
export function insightsFetch(route:RequestInfo|URL,init?:RequestInit):Promise<Response>{if(!transport)throw new Error('Multica insight transport is not initialized');return transport(route,init)}
