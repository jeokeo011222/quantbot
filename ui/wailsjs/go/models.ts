export namespace broker {
	
	export class Asset {
	    Cash: number;
	    MarketValue: number;
	    TotalAssets: number;
	    Frozen: number;
	    Available: number;
	    ExternalMessage: string;
	
	    static createFrom(source: any = {}) {
	        return new Asset(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.Cash = source["Cash"];
	        this.MarketValue = source["MarketValue"];
	        this.TotalAssets = source["TotalAssets"];
	        this.Frozen = source["Frozen"];
	        this.Available = source["Available"];
	        this.ExternalMessage = source["ExternalMessage"];
	    }
	}
	export class Position {
	    Symbol: string;
	    Quantity: number;
	    Available: number;
	    CostPrice: number;
	    Market: string;
	
	    static createFrom(source: any = {}) {
	        return new Position(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.Symbol = source["Symbol"];
	        this.Quantity = source["Quantity"];
	        this.Available = source["Available"];
	        this.CostPrice = source["CostPrice"];
	        this.Market = source["Market"];
	    }
	}
	export class Status {
	    mode: string;
	    connected: boolean;
	    python_ok: boolean;
	    xtquant_ok: boolean;
	    account: string;
	    cash: number;
	    market_value: number;
	    message: string;
	    python_path: string;
	
	    static createFrom(source: any = {}) {
	        return new Status(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.mode = source["mode"];
	        this.connected = source["connected"];
	        this.python_ok = source["python_ok"];
	        this.xtquant_ok = source["xtquant_ok"];
	        this.account = source["account"];
	        this.cash = source["cash"];
	        this.market_value = source["market_value"];
	        this.message = source["message"];
	        this.python_path = source["python_path"];
	    }
	}

}

export namespace config {
	
	export class SourceToggle {
	    enabled: boolean;
	    priority: number;
	
	    static createFrom(source: any = {}) {
	        return new SourceToggle(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.enabled = source["enabled"];
	        this.priority = source["priority"];
	    }
	}
	export class SixDimSourceConfig {
	    northbound: SourceToggle;
	    margin: SourceToggle;
	    volume_expansion: SourceToggle;
	    ths_boards: SourceToggle;
	    limit_board: SourceToggle;
	    full_market_stats: SourceToggle;
	    overnight: SourceToggle;
	
	    static createFrom(source: any = {}) {
	        return new SixDimSourceConfig(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.northbound = this.convertValues(source["northbound"], SourceToggle);
	        this.margin = this.convertValues(source["margin"], SourceToggle);
	        this.volume_expansion = this.convertValues(source["volume_expansion"], SourceToggle);
	        this.ths_boards = this.convertValues(source["ths_boards"], SourceToggle);
	        this.limit_board = this.convertValues(source["limit_board"], SourceToggle);
	        this.full_market_stats = this.convertValues(source["full_market_stats"], SourceToggle);
	        this.overnight = this.convertValues(source["overnight"], SourceToggle);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}

}

export namespace llmstore {
	
	export class Entry {
	    id: number;
	    taskDate: string;
	    agentRole: string;
	    taskName: string;
	    phase: string;
	    model: string;
	    inputMessages: string;
	    outputContent: string;
	    finishReason: string;
	    toolCallCount: number;
	    promptTokens: number;
	    completionTokens: number;
	    totalTokens: number;
	    durationMs: number;
	    status: string;
	    errorMessage: string;
	    // Go type: time
	    createdAt: any;
	
	    static createFrom(source: any = {}) {
	        return new Entry(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.taskDate = source["taskDate"];
	        this.agentRole = source["agentRole"];
	        this.taskName = source["taskName"];
	        this.phase = source["phase"];
	        this.model = source["model"];
	        this.inputMessages = source["inputMessages"];
	        this.outputContent = source["outputContent"];
	        this.finishReason = source["finishReason"];
	        this.toolCallCount = source["toolCallCount"];
	        this.promptTokens = source["promptTokens"];
	        this.completionTokens = source["completionTokens"];
	        this.totalTokens = source["totalTokens"];
	        this.durationMs = source["durationMs"];
	        this.status = source["status"];
	        this.errorMessage = source["errorMessage"];
	        this.createdAt = this.convertValues(source["createdAt"], null);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}

}

export namespace main {
	
	export class OptimizePortfolioRequest {
	    method: string;
	    symbols: string[];
	    totalWeight: number;
	    maxSingle: number;
	    budgets: number[];
	    days: number;
	
	    static createFrom(source: any = {}) {
	        return new OptimizePortfolioRequest(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.method = source["method"];
	        this.symbols = source["symbols"];
	        this.totalWeight = source["totalWeight"];
	        this.maxSingle = source["maxSingle"];
	        this.budgets = source["budgets"];
	        this.days = source["days"];
	    }
	}

}

