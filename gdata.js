(function() {
    const localStorage = window.localStorage;

    const propSeparator = ",,";

    function mangle(propKey) {
        return "$" + propKey + "$";
    }

    function unmangle(propKey) {
        return propKey.substring(1, propKey.length-1);
    }

    function objectPropPath(appKey, objectKey, mangledPropKey) {
        return appKey + "_" + objectKey + "__" + mangledPropKey;
    }

    function objectMetadataPath(appKey, objectKey) {
        return appKey + "_" + objectKey + "_proplist_";
    }

    function listObjectProps(appKey, objectKey) {
        let metaKey = objectMetadataPath(appKey, objectKey);
        let v = localStorage.getItem(metaKey);
        if (v === null) {
            return null;
        }
        if (v === "") {
            return [];
        }
        return v.split(propSeparator).map(unmangle);
    }

    function saveObjectProp(appKey, objectKey, propKey, data) {
        let mangledPropKey = mangle(propKey);
        let metaKey = objectMetadataPath(appKey, objectKey);
        let v = localStorage.getItem(metaKey);
        if (v === null || v === '') {
            // Creating a fresh meta file with a single (new) prop key.
            localStorage.setItem(metaKey, mangledPropKey);
        } else {
            if (!v.includes(mangledPropKey)) {
                v += propSeparator + mangledPropKey;
                localStorage.setItem(metaKey, v);
            }
        }
        localStorage.setItem(objectPropPath(appKey, objectKey, mangledPropKey), data);
    }

    function loadObjectProp(appKey, objectKey, propKey) {
        let mangledPropKey = mangle(propKey);
        return localStorage.getItem(objectPropPath(appKey, objectKey, mangledPropKey));
    }

    function objectPropExists(appKey, objectKey, propKey) {
        let mangledPropKey = mangle(propKey);
        return localStorage.getItem(objectPropPath(appKey, objectKey, mangledPropKey)) !== null;
    }

    function objectExists(appKey, objectKey) {
        return localStorage.getItem(objectMetadataPath(appKey, objectKey)) !== null;
    }

    function deleteObjectProp(appKey, objectKey, propKey) {
        let mangledPropKey = mangle(propKey);
        localStorage.removeItem(objectPropPath(appKey, objectKey, mangledPropKey));

        let metaKey = objectMetadataPath(appKey, objectKey);
        let v = localStorage.getItem(metaKey);
        if (v !== null) {
            let parts = v.split(propSeparator);
            let index = parts.indexOf(mangledPropKey);
            if (index > -1) {
                parts.splice(index, 1);
                localStorage.setItem(metaKey, parts.join(propSeparator));
            }
        }
    }

    function deleteObject(appKey, objectKey) {
        let metaKey = objectMetadataPath(appKey, objectKey);
        let v = localStorage.getItem(metaKey);
        if (v === null) {
            return;
        }

        let keys = v.split(propSeparator);
        for (let i = 0; i < keys.length; i++) {
            let mangledPropKey = keys[i];
            localStorage.removeItem(objectPropPath(appKey, objectKey, mangledPropKey));
        }
        localStorage.removeItem(metaKey);
    }

    // === Safe-slot storage primitives =====================================
    //
    // The slot engine (implemented in Go) only deals with forward-slash
    // relative paths. Every slot value lives under its own prefix so it can
    // never collide with a legacy localStorage key. Frames are opaque
    // base64 strings to this layer; all encoding and validation happen in Go.

    const slotPrefix = "gslot__";

    function slotKey(appKey, relPath) {
        return slotPrefix + appKey + "/" + relPath;
    }

    function slotWrite(appKey, relPath, value) {
        try {
            localStorage.setItem(slotKey(appKey, relPath), value);
            return {ok: true};
        } catch (e) {
            // QuotaExceededError (and its IE-era numeric code 22).
            let noSpace = (e && (e.name === "QuotaExceededError" || e.code === 22));
            return {ok: false, noSpace: noSpace, message: String(e)};
        }
    }

    function slotRead(appKey, relPath) {
        let v = localStorage.getItem(slotKey(appKey, relPath));
        return v === null ? {ok: true, missing: true} : {ok: true, value: v};
    }

    function slotRemove(appKey, relPath) {
        localStorage.removeItem(slotKey(appKey, relPath));
        return {ok: true};
    }

    function slotRemoveAll(appKey, relDir) {
        let prefix = slotKey(appKey, relDir) + "/";
        // Snapshot the keys first: mutating storage while enumerating
        // Storage keys is not portable across browsers.
        let doomed = [];
        for (let i = 0; i < localStorage.length; i++) {
            let k = localStorage.key(i);
            if (k.indexOf(prefix) === 0) {
                doomed.push(k);
            }
        }
        for (let i = 0; i < doomed.length; i++) {
            localStorage.removeItem(doomed[i]);
        }
        return {ok: true};
    }

    function slotList(appKey, relDir) {
        let prefix = slotKey(appKey, relDir) + "/";
        let names = [];
        let seen = {};
        for (let i = 0; i < localStorage.length; i++) {
            let k = localStorage.key(i);
            if (k.indexOf(prefix) !== 0) {
                continue;
            }
            let rest = k.substring(prefix.length);
            // Only direct children of relDir are reported.
            let slash = rest.indexOf("/");
            let name = slash === -1 ? rest : rest.substring(0, slash);
            if (!seen[name]) {
                seen[name] = true;
                names.push(name);
            }
        }
        return {ok: true, names: names};
    }

    return {
        "objectPropPath": objectPropPath,
        "listObjectProps": listObjectProps,
        "saveObjectProp": saveObjectProp,
        "loadObjectProp": loadObjectProp,
        "objectPropExists": objectPropExists,
        "objectExists": objectExists,
        "deleteObjectProp": deleteObjectProp,
        "deleteObject": deleteObject,
        "slotWrite": slotWrite,
        "slotRead": slotRead,
        "slotRemove": slotRemove,
        "slotRemoveAll": slotRemoveAll,
        "slotList": slotList,
    };
}());
