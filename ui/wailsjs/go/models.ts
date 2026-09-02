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

