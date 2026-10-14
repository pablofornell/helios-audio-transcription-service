export namespace main {
	
	export class AudioDTO {
	    filename: string;
	    mediaType: string;
	    sizeBytes: number;
	    durationSeconds: number;
	    sampleRateHz: number;
	    channels: number;
	    codec: string;
	
	    static createFrom(source: any = {}) {
	        return new AudioDTO(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.filename = source["filename"];
	        this.mediaType = source["mediaType"];
	        this.sizeBytes = source["sizeBytes"];
	        this.durationSeconds = source["durationSeconds"];
	        this.sampleRateHz = source["sampleRateHz"];
	        this.channels = source["channels"];
	        this.codec = source["codec"];
	    }
	}
	export class CheckResultDTO {
	    ok: boolean;
	    detail: string;
	    freeBytes?: number;
	    totalBytes?: number;
	
	    static createFrom(source: any = {}) {
	        return new CheckResultDTO(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.ok = source["ok"];
	        this.detail = source["detail"];
	        this.freeBytes = source["freeBytes"];
	        this.totalBytes = source["totalBytes"];
	    }
	}
	export class ErrorDTO {
	    code: string;
	    message: string;
	    details?: Record<string, any>;
	
	    static createFrom(source: any = {}) {
	        return new ErrorDTO(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.code = source["code"];
	        this.message = source["message"];
	        this.details = source["details"];
	    }
	}
	export class HealthReportDTO {
	    ok: boolean;
	    checks: Record<string, CheckResultDTO>;
	
	    static createFrom(source: any = {}) {
	        return new HealthReportDTO(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.ok = source["ok"];
	        this.checks = this.convertValues(source["checks"], CheckResultDTO, true);
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
	export class ListFilterDTO {
	    library?: boolean;
	    status?: string;
	    cursor?: string;
	    limit?: number;
	
	    static createFrom(source: any = {}) {
	        return new ListFilterDTO(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.library = source["library"];
	        this.status = source["status"];
	        this.cursor = source["cursor"];
	        this.limit = source["limit"];
	    }
	}
	export class WordDTO {
	    word: string;
	    start: number;
	    end: number;
	    probability: number;
	
	    static createFrom(source: any = {}) {
	        return new WordDTO(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.word = source["word"];
	        this.start = source["start"];
	        this.end = source["end"];
	        this.probability = source["probability"];
	    }
	}
	export class SegmentDTO {
	    id: number;
	    start: number;
	    end: number;
	    text: string;
	    words?: WordDTO[];
	
	    static createFrom(source: any = {}) {
	        return new SegmentDTO(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.start = source["start"];
	        this.end = source["end"];
	        this.text = source["text"];
	        this.words = this.convertValues(source["words"], WordDTO);
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
	export class ResultDTO {
	    text: string;
	    language: string;
	    durationSeconds: number;
	    model: string;
	    task: string;
	    segments: SegmentDTO[];
	
	    static createFrom(source: any = {}) {
	        return new ResultDTO(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.text = source["text"];
	        this.language = source["language"];
	        this.durationSeconds = source["durationSeconds"];
	        this.model = source["model"];
	        this.task = source["task"];
	        this.segments = this.convertValues(source["segments"], SegmentDTO);
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
	export class OptionsDTO {
	    model: string;
	    language: string;
	    task: string;
	    wordTimestamps: boolean;
	    initialPrompt: string;
	    beamSize: number;
	    temperature: number;
	
	    static createFrom(source: any = {}) {
	        return new OptionsDTO(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.model = source["model"];
	        this.language = source["language"];
	        this.task = source["task"];
	        this.wordTimestamps = source["wordTimestamps"];
	        this.initialPrompt = source["initialPrompt"];
	        this.beamSize = source["beamSize"];
	        this.temperature = source["temperature"];
	    }
	}
	export class TranscriptionDTO {
	    id: string;
	    status: string;
	    options: OptionsDTO;
	    audio: AudioDTO;
	    result?: ResultDTO;
	    error?: ErrorDTO;
	    attempts: number;
	    maxAttempts: number;
	    // Go type: time
	    createdAt: any;
	    // Go type: time
	    updatedAt: any;
	    // Go type: time
	    startedAt?: any;
	    // Go type: time
	    completedAt?: any;
	
	    static createFrom(source: any = {}) {
	        return new TranscriptionDTO(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.status = source["status"];
	        this.options = this.convertValues(source["options"], OptionsDTO);
	        this.audio = this.convertValues(source["audio"], AudioDTO);
	        this.result = this.convertValues(source["result"], ResultDTO);
	        this.error = this.convertValues(source["error"], ErrorDTO);
	        this.attempts = source["attempts"];
	        this.maxAttempts = source["maxAttempts"];
	        this.createdAt = this.convertValues(source["createdAt"], null);
	        this.updatedAt = this.convertValues(source["updatedAt"], null);
	        this.startedAt = this.convertValues(source["startedAt"], null);
	        this.completedAt = this.convertValues(source["completedAt"], null);
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
	export class ListPageDTO {
	    items: TranscriptionDTO[];
	    nextCursor?: string;
	
	    static createFrom(source: any = {}) {
	        return new ListPageDTO(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.items = this.convertValues(source["items"], TranscriptionDTO);
	        this.nextCursor = source["nextCursor"];
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
	export class MediaLocationDTO {
	    path?: string;
	    exists: boolean;
	
	    static createFrom(source: any = {}) {
	        return new MediaLocationDTO(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.path = source["path"];
	        this.exists = source["exists"];
	    }
	}
	
	
	
	export class ServerSettingsDTO {
	    host: string;
	    port: number;
	    scheme: string;
	    baseUrl: string;
	    editable: boolean;
	    warning?: string;
	
	    static createFrom(source: any = {}) {
	        return new ServerSettingsDTO(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.host = source["host"];
	        this.port = source["port"];
	        this.scheme = source["scheme"];
	        this.baseUrl = source["baseUrl"];
	        this.editable = source["editable"];
	        this.warning = source["warning"];
	    }
	}
	export class SubmitRequestDTO {
	    filePath: string;
	    options: OptionsDTO;
	
	    static createFrom(source: any = {}) {
	        return new SubmitRequestDTO(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.filePath = source["filePath"];
	        this.options = this.convertValues(source["options"], OptionsDTO);
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

