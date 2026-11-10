"use strict";
(() => {
  var __create = Object.create;
  var __defProp = Object.defineProperty;
  var __getOwnPropDesc = Object.getOwnPropertyDescriptor;
  var __getOwnPropNames = Object.getOwnPropertyNames;
  var __getProtoOf = Object.getPrototypeOf;
  var __hasOwnProp = Object.prototype.hasOwnProperty;
  var __commonJS = (cb, mod) => function __require() {
    return mod || (0, cb[__getOwnPropNames(cb)[0]])((mod = { exports: {} }).exports, mod), mod.exports;
  };
  var __copyProps = (to, from, except, desc) => {
    if (from && typeof from === "object" || typeof from === "function") {
      for (let key of __getOwnPropNames(from))
        if (!__hasOwnProp.call(to, key) && key !== except)
          __defProp(to, key, { get: () => from[key], enumerable: !(desc = __getOwnPropDesc(from, key)) || desc.enumerable });
    }
    return to;
  };
  var __toESM = (mod, isNodeMode, target) => (target = mod != null ? __create(__getProtoOf(mod)) : {}, __copyProps(
    // If the importer is in node compatibility mode or this is not an ESM
    // file that has been converted to a CommonJS file using a Babel-
    // compatible transform (i.e. "__esModule" has not been set), then set
    // "default" to the CommonJS "module.exports" for node compatibility.
    isNodeMode || !mod || !mod.__esModule ? __defProp(target, "default", { value: mod, enumerable: true }) : target,
    mod
  ));

  // node_modules/qrcode/lib/can-promise.js
  var require_can_promise = __commonJS({
    "node_modules/qrcode/lib/can-promise.js"(exports, module) {
      module.exports = function() {
        return typeof Promise === "function" && Promise.prototype && Promise.prototype.then;
      };
    }
  });

  // node_modules/qrcode/lib/core/utils.js
  var require_utils = __commonJS({
    "node_modules/qrcode/lib/core/utils.js"(exports) {
      var toSJISFunction;
      var CODEWORDS_COUNT = [
        0,
        // Not used
        26,
        44,
        70,
        100,
        134,
        172,
        196,
        242,
        292,
        346,
        404,
        466,
        532,
        581,
        655,
        733,
        815,
        901,
        991,
        1085,
        1156,
        1258,
        1364,
        1474,
        1588,
        1706,
        1828,
        1921,
        2051,
        2185,
        2323,
        2465,
        2611,
        2761,
        2876,
        3034,
        3196,
        3362,
        3532,
        3706
      ];
      exports.getSymbolSize = function getSymbolSize(version) {
        if (!version) throw new Error('"version" cannot be null or undefined');
        if (version < 1 || version > 40) throw new Error('"version" should be in range from 1 to 40');
        return version * 4 + 17;
      };
      exports.getSymbolTotalCodewords = function getSymbolTotalCodewords(version) {
        return CODEWORDS_COUNT[version];
      };
      exports.getBCHDigit = function(data) {
        let digit = 0;
        while (data !== 0) {
          digit++;
          data >>>= 1;
        }
        return digit;
      };
      exports.setToSJISFunction = function setToSJISFunction(f) {
        if (typeof f !== "function") {
          throw new Error('"toSJISFunc" is not a valid function.');
        }
        toSJISFunction = f;
      };
      exports.isKanjiModeEnabled = function() {
        return typeof toSJISFunction !== "undefined";
      };
      exports.toSJIS = function toSJIS(kanji) {
        return toSJISFunction(kanji);
      };
    }
  });

  // node_modules/qrcode/lib/core/error-correction-level.js
  var require_error_correction_level = __commonJS({
    "node_modules/qrcode/lib/core/error-correction-level.js"(exports) {
      exports.L = { bit: 1 };
      exports.M = { bit: 0 };
      exports.Q = { bit: 3 };
      exports.H = { bit: 2 };
      function fromString(string) {
        if (typeof string !== "string") {
          throw new Error("Param is not a string");
        }
        const lcStr = string.toLowerCase();
        switch (lcStr) {
          case "l":
          case "low":
            return exports.L;
          case "m":
          case "medium":
            return exports.M;
          case "q":
          case "quartile":
            return exports.Q;
          case "h":
          case "high":
            return exports.H;
          default:
            throw new Error("Unknown EC Level: " + string);
        }
      }
      exports.isValid = function isValid(level) {
        return level && typeof level.bit !== "undefined" && level.bit >= 0 && level.bit < 4;
      };
      exports.from = function from(value, defaultValue) {
        if (exports.isValid(value)) {
          return value;
        }
        try {
          return fromString(value);
        } catch (e) {
          return defaultValue;
        }
      };
    }
  });

  // node_modules/qrcode/lib/core/bit-buffer.js
  var require_bit_buffer = __commonJS({
    "node_modules/qrcode/lib/core/bit-buffer.js"(exports, module) {
      function BitBuffer() {
        this.buffer = [];
        this.length = 0;
      }
      BitBuffer.prototype = {
        get: function(index) {
          const bufIndex = Math.floor(index / 8);
          return (this.buffer[bufIndex] >>> 7 - index % 8 & 1) === 1;
        },
        put: function(num, length) {
          for (let i = 0; i < length; i++) {
            this.putBit((num >>> length - i - 1 & 1) === 1);
          }
        },
        getLengthInBits: function() {
          return this.length;
        },
        putBit: function(bit) {
          const bufIndex = Math.floor(this.length / 8);
          if (this.buffer.length <= bufIndex) {
            this.buffer.push(0);
          }
          if (bit) {
            this.buffer[bufIndex] |= 128 >>> this.length % 8;
          }
          this.length++;
        }
      };
      module.exports = BitBuffer;
    }
  });

  // node_modules/qrcode/lib/core/bit-matrix.js
  var require_bit_matrix = __commonJS({
    "node_modules/qrcode/lib/core/bit-matrix.js"(exports, module) {
      function BitMatrix(size) {
        if (!size || size < 1) {
          throw new Error("BitMatrix size must be defined and greater than 0");
        }
        this.size = size;
        this.data = new Uint8Array(size * size);
        this.reservedBit = new Uint8Array(size * size);
      }
      BitMatrix.prototype.set = function(row, col, value, reserved) {
        const index = row * this.size + col;
        this.data[index] = value;
        if (reserved) this.reservedBit[index] = true;
      };
      BitMatrix.prototype.get = function(row, col) {
        return this.data[row * this.size + col];
      };
      BitMatrix.prototype.xor = function(row, col, value) {
        this.data[row * this.size + col] ^= value;
      };
      BitMatrix.prototype.isReserved = function(row, col) {
        return this.reservedBit[row * this.size + col];
      };
      module.exports = BitMatrix;
    }
  });

  // node_modules/qrcode/lib/core/alignment-pattern.js
  var require_alignment_pattern = __commonJS({
    "node_modules/qrcode/lib/core/alignment-pattern.js"(exports) {
      var getSymbolSize = require_utils().getSymbolSize;
      exports.getRowColCoords = function getRowColCoords(version) {
        if (version === 1) return [];
        const posCount = Math.floor(version / 7) + 2;
        const size = getSymbolSize(version);
        const intervals = size === 145 ? 26 : Math.ceil((size - 13) / (2 * posCount - 2)) * 2;
        const positions = [size - 7];
        for (let i = 1; i < posCount - 1; i++) {
          positions[i] = positions[i - 1] - intervals;
        }
        positions.push(6);
        return positions.reverse();
      };
      exports.getPositions = function getPositions(version) {
        const coords = [];
        const pos = exports.getRowColCoords(version);
        const posLength = pos.length;
        for (let i = 0; i < posLength; i++) {
          for (let j = 0; j < posLength; j++) {
            if (i === 0 && j === 0 || // top-left
            i === 0 && j === posLength - 1 || // bottom-left
            i === posLength - 1 && j === 0) {
              continue;
            }
            coords.push([pos[i], pos[j]]);
          }
        }
        return coords;
      };
    }
  });

  // node_modules/qrcode/lib/core/finder-pattern.js
  var require_finder_pattern = __commonJS({
    "node_modules/qrcode/lib/core/finder-pattern.js"(exports) {
      var getSymbolSize = require_utils().getSymbolSize;
      var FINDER_PATTERN_SIZE = 7;
      exports.getPositions = function getPositions(version) {
        const size = getSymbolSize(version);
        return [
          // top-left
          [0, 0],
          // top-right
          [size - FINDER_PATTERN_SIZE, 0],
          // bottom-left
          [0, size - FINDER_PATTERN_SIZE]
        ];
      };
    }
  });

  // node_modules/qrcode/lib/core/mask-pattern.js
  var require_mask_pattern = __commonJS({
    "node_modules/qrcode/lib/core/mask-pattern.js"(exports) {
      exports.Patterns = {
        PATTERN000: 0,
        PATTERN001: 1,
        PATTERN010: 2,
        PATTERN011: 3,
        PATTERN100: 4,
        PATTERN101: 5,
        PATTERN110: 6,
        PATTERN111: 7
      };
      var PenaltyScores = {
        N1: 3,
        N2: 3,
        N3: 40,
        N4: 10
      };
      exports.isValid = function isValid(mask) {
        return mask != null && mask !== "" && !isNaN(mask) && mask >= 0 && mask <= 7;
      };
      exports.from = function from(value) {
        return exports.isValid(value) ? parseInt(value, 10) : void 0;
      };
      exports.getPenaltyN1 = function getPenaltyN1(data) {
        const size = data.size;
        let points = 0;
        let sameCountCol = 0;
        let sameCountRow = 0;
        let lastCol = null;
        let lastRow = null;
        for (let row = 0; row < size; row++) {
          sameCountCol = sameCountRow = 0;
          lastCol = lastRow = null;
          for (let col = 0; col < size; col++) {
            let module2 = data.get(row, col);
            if (module2 === lastCol) {
              sameCountCol++;
            } else {
              if (sameCountCol >= 5) points += PenaltyScores.N1 + (sameCountCol - 5);
              lastCol = module2;
              sameCountCol = 1;
            }
            module2 = data.get(col, row);
            if (module2 === lastRow) {
              sameCountRow++;
            } else {
              if (sameCountRow >= 5) points += PenaltyScores.N1 + (sameCountRow - 5);
              lastRow = module2;
              sameCountRow = 1;
            }
          }
          if (sameCountCol >= 5) points += PenaltyScores.N1 + (sameCountCol - 5);
          if (sameCountRow >= 5) points += PenaltyScores.N1 + (sameCountRow - 5);
        }
        return points;
      };
      exports.getPenaltyN2 = function getPenaltyN2(data) {
        const size = data.size;
        let points = 0;
        for (let row = 0; row < size - 1; row++) {
          for (let col = 0; col < size - 1; col++) {
            const last = data.get(row, col) + data.get(row, col + 1) + data.get(row + 1, col) + data.get(row + 1, col + 1);
            if (last === 4 || last === 0) points++;
          }
        }
        return points * PenaltyScores.N2;
      };
      exports.getPenaltyN3 = function getPenaltyN3(data) {
        const size = data.size;
        let points = 0;
        let bitsCol = 0;
        let bitsRow = 0;
        for (let row = 0; row < size; row++) {
          bitsCol = bitsRow = 0;
          for (let col = 0; col < size; col++) {
            bitsCol = bitsCol << 1 & 2047 | data.get(row, col);
            if (col >= 10 && (bitsCol === 1488 || bitsCol === 93)) points++;
            bitsRow = bitsRow << 1 & 2047 | data.get(col, row);
            if (col >= 10 && (bitsRow === 1488 || bitsRow === 93)) points++;
          }
        }
        return points * PenaltyScores.N3;
      };
      exports.getPenaltyN4 = function getPenaltyN4(data) {
        let darkCount = 0;
        const modulesCount = data.data.length;
        for (let i = 0; i < modulesCount; i++) darkCount += data.data[i];
        const k = Math.abs(Math.ceil(darkCount * 100 / modulesCount / 5) - 10);
        return k * PenaltyScores.N4;
      };
      function getMaskAt(maskPattern, i, j) {
        switch (maskPattern) {
          case exports.Patterns.PATTERN000:
            return (i + j) % 2 === 0;
          case exports.Patterns.PATTERN001:
            return i % 2 === 0;
          case exports.Patterns.PATTERN010:
            return j % 3 === 0;
          case exports.Patterns.PATTERN011:
            return (i + j) % 3 === 0;
          case exports.Patterns.PATTERN100:
            return (Math.floor(i / 2) + Math.floor(j / 3)) % 2 === 0;
          case exports.Patterns.PATTERN101:
            return i * j % 2 + i * j % 3 === 0;
          case exports.Patterns.PATTERN110:
            return (i * j % 2 + i * j % 3) % 2 === 0;
          case exports.Patterns.PATTERN111:
            return (i * j % 3 + (i + j) % 2) % 2 === 0;
          default:
            throw new Error("bad maskPattern:" + maskPattern);
        }
      }
      exports.applyMask = function applyMask(pattern, data) {
        const size = data.size;
        for (let col = 0; col < size; col++) {
          for (let row = 0; row < size; row++) {
            if (data.isReserved(row, col)) continue;
            data.xor(row, col, getMaskAt(pattern, row, col));
          }
        }
      };
      exports.getBestMask = function getBestMask(data, setupFormatFunc) {
        const numPatterns = Object.keys(exports.Patterns).length;
        let bestPattern = 0;
        let lowerPenalty = Infinity;
        for (let p = 0; p < numPatterns; p++) {
          setupFormatFunc(p);
          exports.applyMask(p, data);
          const penalty = exports.getPenaltyN1(data) + exports.getPenaltyN2(data) + exports.getPenaltyN3(data) + exports.getPenaltyN4(data);
          exports.applyMask(p, data);
          if (penalty < lowerPenalty) {
            lowerPenalty = penalty;
            bestPattern = p;
          }
        }
        return bestPattern;
      };
    }
  });

  // node_modules/qrcode/lib/core/error-correction-code.js
  var require_error_correction_code = __commonJS({
    "node_modules/qrcode/lib/core/error-correction-code.js"(exports) {
      var ECLevel = require_error_correction_level();
      var EC_BLOCKS_TABLE = [
        // L  M  Q  H
        1,
        1,
        1,
        1,
        1,
        1,
        1,
        1,
        1,
        1,
        2,
        2,
        1,
        2,
        2,
        4,
        1,
        2,
        4,
        4,
        2,
        4,
        4,
        4,
        2,
        4,
        6,
        5,
        2,
        4,
        6,
        6,
        2,
        5,
        8,
        8,
        4,
        5,
        8,
        8,
        4,
        5,
        8,
        11,
        4,
        8,
        10,
        11,
        4,
        9,
        12,
        16,
        4,
        9,
        16,
        16,
        6,
        10,
        12,
        18,
        6,
        10,
        17,
        16,
        6,
        11,
        16,
        19,
        6,
        13,
        18,
        21,
        7,
        14,
        21,
        25,
        8,
        16,
        20,
        25,
        8,
        17,
        23,
        25,
        9,
        17,
        23,
        34,
        9,
        18,
        25,
        30,
        10,
        20,
        27,
        32,
        12,
        21,
        29,
        35,
        12,
        23,
        34,
        37,
        12,
        25,
        34,
        40,
        13,
        26,
        35,
        42,
        14,
        28,
        38,
        45,
        15,
        29,
        40,
        48,
        16,
        31,
        43,
        51,
        17,
        33,
        45,
        54,
        18,
        35,
        48,
        57,
        19,
        37,
        51,
        60,
        19,
        38,
        53,
        63,
        20,
        40,
        56,
        66,
        21,
        43,
        59,
        70,
        22,
        45,
        62,
        74,
        24,
        47,
        65,
        77,
        25,
        49,
        68,
        81
      ];
      var EC_CODEWORDS_TABLE = [
        // L  M  Q  H
        7,
        10,
        13,
        17,
        10,
        16,
        22,
        28,
        15,
        26,
        36,
        44,
        20,
        36,
        52,
        64,
        26,
        48,
        72,
        88,
        36,
        64,
        96,
        112,
        40,
        72,
        108,
        130,
        48,
        88,
        132,
        156,
        60,
        110,
        160,
        192,
        72,
        130,
        192,
        224,
        80,
        150,
        224,
        264,
        96,
        176,
        260,
        308,
        104,
        198,
        288,
        352,
        120,
        216,
        320,
        384,
        132,
        240,
        360,
        432,
        144,
        280,
        408,
        480,
        168,
        308,
        448,
        532,
        180,
        338,
        504,
        588,
        196,
        364,
        546,
        650,
        224,
        416,
        600,
        700,
        224,
        442,
        644,
        750,
        252,
        476,
        690,
        816,
        270,
        504,
        750,
        900,
        300,
        560,
        810,
        960,
        312,
        588,
        870,
        1050,
        336,
        644,
        952,
        1110,
        360,
        700,
        1020,
        1200,
        390,
        728,
        1050,
        1260,
        420,
        784,
        1140,
        1350,
        450,
        812,
        1200,
        1440,
        480,
        868,
        1290,
        1530,
        510,
        924,
        1350,
        1620,
        540,
        980,
        1440,
        1710,
        570,
        1036,
        1530,
        1800,
        570,
        1064,
        1590,
        1890,
        600,
        1120,
        1680,
        1980,
        630,
        1204,
        1770,
        2100,
        660,
        1260,
        1860,
        2220,
        720,
        1316,
        1950,
        2310,
        750,
        1372,
        2040,
        2430
      ];
      exports.getBlocksCount = function getBlocksCount(version, errorCorrectionLevel) {
        switch (errorCorrectionLevel) {
          case ECLevel.L:
            return EC_BLOCKS_TABLE[(version - 1) * 4 + 0];
          case ECLevel.M:
            return EC_BLOCKS_TABLE[(version - 1) * 4 + 1];
          case ECLevel.Q:
            return EC_BLOCKS_TABLE[(version - 1) * 4 + 2];
          case ECLevel.H:
            return EC_BLOCKS_TABLE[(version - 1) * 4 + 3];
          default:
            return void 0;
        }
      };
      exports.getTotalCodewordsCount = function getTotalCodewordsCount(version, errorCorrectionLevel) {
        switch (errorCorrectionLevel) {
          case ECLevel.L:
            return EC_CODEWORDS_TABLE[(version - 1) * 4 + 0];
          case ECLevel.M:
            return EC_CODEWORDS_TABLE[(version - 1) * 4 + 1];
          case ECLevel.Q:
            return EC_CODEWORDS_TABLE[(version - 1) * 4 + 2];
          case ECLevel.H:
            return EC_CODEWORDS_TABLE[(version - 1) * 4 + 3];
          default:
            return void 0;
        }
      };
    }
  });

  // node_modules/qrcode/lib/core/galois-field.js
  var require_galois_field = __commonJS({
    "node_modules/qrcode/lib/core/galois-field.js"(exports) {
      var EXP_TABLE = new Uint8Array(512);
      var LOG_TABLE = new Uint8Array(256);
      (function initTables() {
        let x = 1;
        for (let i = 0; i < 255; i++) {
          EXP_TABLE[i] = x;
          LOG_TABLE[x] = i;
          x <<= 1;
          if (x & 256) {
            x ^= 285;
          }
        }
        for (let i = 255; i < 512; i++) {
          EXP_TABLE[i] = EXP_TABLE[i - 255];
        }
      })();
      exports.log = function log(n) {
        if (n < 1) throw new Error("log(" + n + ")");
        return LOG_TABLE[n];
      };
      exports.exp = function exp(n) {
        return EXP_TABLE[n];
      };
      exports.mul = function mul(x, y) {
        if (x === 0 || y === 0) return 0;
        return EXP_TABLE[LOG_TABLE[x] + LOG_TABLE[y]];
      };
    }
  });

  // node_modules/qrcode/lib/core/polynomial.js
  var require_polynomial = __commonJS({
    "node_modules/qrcode/lib/core/polynomial.js"(exports) {
      var GF = require_galois_field();
      exports.mul = function mul(p1, p2) {
        const coeff = new Uint8Array(p1.length + p2.length - 1);
        for (let i = 0; i < p1.length; i++) {
          for (let j = 0; j < p2.length; j++) {
            coeff[i + j] ^= GF.mul(p1[i], p2[j]);
          }
        }
        return coeff;
      };
      exports.mod = function mod(divident, divisor) {
        let result = new Uint8Array(divident);
        while (result.length - divisor.length >= 0) {
          const coeff = result[0];
          for (let i = 0; i < divisor.length; i++) {
            result[i] ^= GF.mul(divisor[i], coeff);
          }
          let offset = 0;
          while (offset < result.length && result[offset] === 0) offset++;
          result = result.slice(offset);
        }
        return result;
      };
      exports.generateECPolynomial = function generateECPolynomial(degree) {
        let poly = new Uint8Array([1]);
        for (let i = 0; i < degree; i++) {
          poly = exports.mul(poly, new Uint8Array([1, GF.exp(i)]));
        }
        return poly;
      };
    }
  });

  // node_modules/qrcode/lib/core/reed-solomon-encoder.js
  var require_reed_solomon_encoder = __commonJS({
    "node_modules/qrcode/lib/core/reed-solomon-encoder.js"(exports, module) {
      var Polynomial = require_polynomial();
      function ReedSolomonEncoder(degree) {
        this.genPoly = void 0;
        this.degree = degree;
        if (this.degree) this.initialize(this.degree);
      }
      ReedSolomonEncoder.prototype.initialize = function initialize(degree) {
        this.degree = degree;
        this.genPoly = Polynomial.generateECPolynomial(this.degree);
      };
      ReedSolomonEncoder.prototype.encode = function encode(data) {
        if (!this.genPoly) {
          throw new Error("Encoder not initialized");
        }
        const paddedData = new Uint8Array(data.length + this.degree);
        paddedData.set(data);
        const remainder = Polynomial.mod(paddedData, this.genPoly);
        const start = this.degree - remainder.length;
        if (start > 0) {
          const buff = new Uint8Array(this.degree);
          buff.set(remainder, start);
          return buff;
        }
        return remainder;
      };
      module.exports = ReedSolomonEncoder;
    }
  });

  // node_modules/qrcode/lib/core/version-check.js
  var require_version_check = __commonJS({
    "node_modules/qrcode/lib/core/version-check.js"(exports) {
      exports.isValid = function isValid(version) {
        return !isNaN(version) && version >= 1 && version <= 40;
      };
    }
  });

  // node_modules/qrcode/lib/core/regex.js
  var require_regex = __commonJS({
    "node_modules/qrcode/lib/core/regex.js"(exports) {
      var numeric = "[0-9]+";
      var alphanumeric = "[A-Z $%*+\\-./:]+";
      var kanji = "(?:[u3000-u303F]|[u3040-u309F]|[u30A0-u30FF]|[uFF00-uFFEF]|[u4E00-u9FAF]|[u2605-u2606]|[u2190-u2195]|u203B|[u2010u2015u2018u2019u2025u2026u201Cu201Du2225u2260]|[u0391-u0451]|[u00A7u00A8u00B1u00B4u00D7u00F7])+";
      kanji = kanji.replace(/u/g, "\\u");
      var byte = "(?:(?![A-Z0-9 $%*+\\-./:]|" + kanji + ")(?:.|[\r\n]))+";
      exports.KANJI = new RegExp(kanji, "g");
      exports.BYTE_KANJI = new RegExp("[^A-Z0-9 $%*+\\-./:]+", "g");
      exports.BYTE = new RegExp(byte, "g");
      exports.NUMERIC = new RegExp(numeric, "g");
      exports.ALPHANUMERIC = new RegExp(alphanumeric, "g");
      var TEST_KANJI = new RegExp("^" + kanji + "$");
      var TEST_NUMERIC = new RegExp("^" + numeric + "$");
      var TEST_ALPHANUMERIC = new RegExp("^[A-Z0-9 $%*+\\-./:]+$");
      exports.testKanji = function testKanji(str) {
        return TEST_KANJI.test(str);
      };
      exports.testNumeric = function testNumeric(str) {
        return TEST_NUMERIC.test(str);
      };
      exports.testAlphanumeric = function testAlphanumeric(str) {
        return TEST_ALPHANUMERIC.test(str);
      };
    }
  });

  // node_modules/qrcode/lib/core/mode.js
  var require_mode = __commonJS({
    "node_modules/qrcode/lib/core/mode.js"(exports) {
      var VersionCheck = require_version_check();
      var Regex = require_regex();
      exports.NUMERIC = {
        id: "Numeric",
        bit: 1 << 0,
        ccBits: [10, 12, 14]
      };
      exports.ALPHANUMERIC = {
        id: "Alphanumeric",
        bit: 1 << 1,
        ccBits: [9, 11, 13]
      };
      exports.BYTE = {
        id: "Byte",
        bit: 1 << 2,
        ccBits: [8, 16, 16]
      };
      exports.KANJI = {
        id: "Kanji",
        bit: 1 << 3,
        ccBits: [8, 10, 12]
      };
      exports.MIXED = {
        bit: -1
      };
      exports.getCharCountIndicator = function getCharCountIndicator(mode, version) {
        if (!mode.ccBits) throw new Error("Invalid mode: " + mode);
        if (!VersionCheck.isValid(version)) {
          throw new Error("Invalid version: " + version);
        }
        if (version >= 1 && version < 10) return mode.ccBits[0];
        else if (version < 27) return mode.ccBits[1];
        return mode.ccBits[2];
      };
      exports.getBestModeForData = function getBestModeForData(dataStr) {
        if (Regex.testNumeric(dataStr)) return exports.NUMERIC;
        else if (Regex.testAlphanumeric(dataStr)) return exports.ALPHANUMERIC;
        else if (Regex.testKanji(dataStr)) return exports.KANJI;
        else return exports.BYTE;
      };
      exports.toString = function toString(mode) {
        if (mode && mode.id) return mode.id;
        throw new Error("Invalid mode");
      };
      exports.isValid = function isValid(mode) {
        return mode && mode.bit && mode.ccBits;
      };
      function fromString(string) {
        if (typeof string !== "string") {
          throw new Error("Param is not a string");
        }
        const lcStr = string.toLowerCase();
        switch (lcStr) {
          case "numeric":
            return exports.NUMERIC;
          case "alphanumeric":
            return exports.ALPHANUMERIC;
          case "kanji":
            return exports.KANJI;
          case "byte":
            return exports.BYTE;
          default:
            throw new Error("Unknown mode: " + string);
        }
      }
      exports.from = function from(value, defaultValue) {
        if (exports.isValid(value)) {
          return value;
        }
        try {
          return fromString(value);
        } catch (e) {
          return defaultValue;
        }
      };
    }
  });

  // node_modules/qrcode/lib/core/version.js
  var require_version = __commonJS({
    "node_modules/qrcode/lib/core/version.js"(exports) {
      var Utils = require_utils();
      var ECCode = require_error_correction_code();
      var ECLevel = require_error_correction_level();
      var Mode = require_mode();
      var VersionCheck = require_version_check();
      var G18 = 1 << 12 | 1 << 11 | 1 << 10 | 1 << 9 | 1 << 8 | 1 << 5 | 1 << 2 | 1 << 0;
      var G18_BCH = Utils.getBCHDigit(G18);
      function getBestVersionForDataLength(mode, length, errorCorrectionLevel) {
        for (let currentVersion = 1; currentVersion <= 40; currentVersion++) {
          if (length <= exports.getCapacity(currentVersion, errorCorrectionLevel, mode)) {
            return currentVersion;
          }
        }
        return void 0;
      }
      function getReservedBitsCount(mode, version) {
        return Mode.getCharCountIndicator(mode, version) + 4;
      }
      function getTotalBitsFromDataArray(segments, version) {
        let totalBits = 0;
        segments.forEach(function(data) {
          const reservedBits = getReservedBitsCount(data.mode, version);
          totalBits += reservedBits + data.getBitsLength();
        });
        return totalBits;
      }
      function getBestVersionForMixedData(segments, errorCorrectionLevel) {
        for (let currentVersion = 1; currentVersion <= 40; currentVersion++) {
          const length = getTotalBitsFromDataArray(segments, currentVersion);
          if (length <= exports.getCapacity(currentVersion, errorCorrectionLevel, Mode.MIXED)) {
            return currentVersion;
          }
        }
        return void 0;
      }
      exports.from = function from(value, defaultValue) {
        if (VersionCheck.isValid(value)) {
          return parseInt(value, 10);
        }
        return defaultValue;
      };
      exports.getCapacity = function getCapacity(version, errorCorrectionLevel, mode) {
        if (!VersionCheck.isValid(version)) {
          throw new Error("Invalid QR Code version");
        }
        if (typeof mode === "undefined") mode = Mode.BYTE;
        const totalCodewords = Utils.getSymbolTotalCodewords(version);
        const ecTotalCodewords = ECCode.getTotalCodewordsCount(version, errorCorrectionLevel);
        const dataTotalCodewordsBits = (totalCodewords - ecTotalCodewords) * 8;
        if (mode === Mode.MIXED) return dataTotalCodewordsBits;
        const usableBits = dataTotalCodewordsBits - getReservedBitsCount(mode, version);
        switch (mode) {
          case Mode.NUMERIC:
            return Math.floor(usableBits / 10 * 3);
          case Mode.ALPHANUMERIC:
            return Math.floor(usableBits / 11 * 2);
          case Mode.KANJI:
            return Math.floor(usableBits / 13);
          case Mode.BYTE:
          default:
            return Math.floor(usableBits / 8);
        }
      };
      exports.getBestVersionForData = function getBestVersionForData(data, errorCorrectionLevel) {
        let seg;
        const ecl = ECLevel.from(errorCorrectionLevel, ECLevel.M);
        if (Array.isArray(data)) {
          if (data.length > 1) {
            return getBestVersionForMixedData(data, ecl);
          }
          if (data.length === 0) {
            return 1;
          }
          seg = data[0];
        } else {
          seg = data;
        }
        return getBestVersionForDataLength(seg.mode, seg.getLength(), ecl);
      };
      exports.getEncodedBits = function getEncodedBits(version) {
        if (!VersionCheck.isValid(version) || version < 7) {
          throw new Error("Invalid QR Code version");
        }
        let d = version << 12;
        while (Utils.getBCHDigit(d) - G18_BCH >= 0) {
          d ^= G18 << Utils.getBCHDigit(d) - G18_BCH;
        }
        return version << 12 | d;
      };
    }
  });

  // node_modules/qrcode/lib/core/format-info.js
  var require_format_info = __commonJS({
    "node_modules/qrcode/lib/core/format-info.js"(exports) {
      var Utils = require_utils();
      var G15 = 1 << 10 | 1 << 8 | 1 << 5 | 1 << 4 | 1 << 2 | 1 << 1 | 1 << 0;
      var G15_MASK = 1 << 14 | 1 << 12 | 1 << 10 | 1 << 4 | 1 << 1;
      var G15_BCH = Utils.getBCHDigit(G15);
      exports.getEncodedBits = function getEncodedBits(errorCorrectionLevel, mask) {
        const data = errorCorrectionLevel.bit << 3 | mask;
        let d = data << 10;
        while (Utils.getBCHDigit(d) - G15_BCH >= 0) {
          d ^= G15 << Utils.getBCHDigit(d) - G15_BCH;
        }
        return (data << 10 | d) ^ G15_MASK;
      };
    }
  });

  // node_modules/qrcode/lib/core/numeric-data.js
  var require_numeric_data = __commonJS({
    "node_modules/qrcode/lib/core/numeric-data.js"(exports, module) {
      var Mode = require_mode();
      function NumericData(data) {
        this.mode = Mode.NUMERIC;
        this.data = data.toString();
      }
      NumericData.getBitsLength = function getBitsLength(length) {
        return 10 * Math.floor(length / 3) + (length % 3 ? length % 3 * 3 + 1 : 0);
      };
      NumericData.prototype.getLength = function getLength() {
        return this.data.length;
      };
      NumericData.prototype.getBitsLength = function getBitsLength() {
        return NumericData.getBitsLength(this.data.length);
      };
      NumericData.prototype.write = function write(bitBuffer) {
        let i, group, value;
        for (i = 0; i + 3 <= this.data.length; i += 3) {
          group = this.data.substr(i, 3);
          value = parseInt(group, 10);
          bitBuffer.put(value, 10);
        }
        const remainingNum = this.data.length - i;
        if (remainingNum > 0) {
          group = this.data.substr(i);
          value = parseInt(group, 10);
          bitBuffer.put(value, remainingNum * 3 + 1);
        }
      };
      module.exports = NumericData;
    }
  });

  // node_modules/qrcode/lib/core/alphanumeric-data.js
  var require_alphanumeric_data = __commonJS({
    "node_modules/qrcode/lib/core/alphanumeric-data.js"(exports, module) {
      var Mode = require_mode();
      var ALPHA_NUM_CHARS = [
        "0",
        "1",
        "2",
        "3",
        "4",
        "5",
        "6",
        "7",
        "8",
        "9",
        "A",
        "B",
        "C",
        "D",
        "E",
        "F",
        "G",
        "H",
        "I",
        "J",
        "K",
        "L",
        "M",
        "N",
        "O",
        "P",
        "Q",
        "R",
        "S",
        "T",
        "U",
        "V",
        "W",
        "X",
        "Y",
        "Z",
        " ",
        "$",
        "%",
        "*",
        "+",
        "-",
        ".",
        "/",
        ":"
      ];
      function AlphanumericData(data) {
        this.mode = Mode.ALPHANUMERIC;
        this.data = data;
      }
      AlphanumericData.getBitsLength = function getBitsLength(length) {
        return 11 * Math.floor(length / 2) + 6 * (length % 2);
      };
      AlphanumericData.prototype.getLength = function getLength() {
        return this.data.length;
      };
      AlphanumericData.prototype.getBitsLength = function getBitsLength() {
        return AlphanumericData.getBitsLength(this.data.length);
      };
      AlphanumericData.prototype.write = function write(bitBuffer) {
        let i;
        for (i = 0; i + 2 <= this.data.length; i += 2) {
          let value = ALPHA_NUM_CHARS.indexOf(this.data[i]) * 45;
          value += ALPHA_NUM_CHARS.indexOf(this.data[i + 1]);
          bitBuffer.put(value, 11);
        }
        if (this.data.length % 2) {
          bitBuffer.put(ALPHA_NUM_CHARS.indexOf(this.data[i]), 6);
        }
      };
      module.exports = AlphanumericData;
    }
  });

  // node_modules/qrcode/lib/core/byte-data.js
  var require_byte_data = __commonJS({
    "node_modules/qrcode/lib/core/byte-data.js"(exports, module) {
      var Mode = require_mode();
      function ByteData(data) {
        this.mode = Mode.BYTE;
        if (typeof data === "string") {
          this.data = new TextEncoder().encode(data);
        } else {
          this.data = new Uint8Array(data);
        }
      }
      ByteData.getBitsLength = function getBitsLength(length) {
        return length * 8;
      };
      ByteData.prototype.getLength = function getLength() {
        return this.data.length;
      };
      ByteData.prototype.getBitsLength = function getBitsLength() {
        return ByteData.getBitsLength(this.data.length);
      };
      ByteData.prototype.write = function(bitBuffer) {
        for (let i = 0, l = this.data.length; i < l; i++) {
          bitBuffer.put(this.data[i], 8);
        }
      };
      module.exports = ByteData;
    }
  });

  // node_modules/qrcode/lib/core/kanji-data.js
  var require_kanji_data = __commonJS({
    "node_modules/qrcode/lib/core/kanji-data.js"(exports, module) {
      var Mode = require_mode();
      var Utils = require_utils();
      function KanjiData(data) {
        this.mode = Mode.KANJI;
        this.data = data;
      }
      KanjiData.getBitsLength = function getBitsLength(length) {
        return length * 13;
      };
      KanjiData.prototype.getLength = function getLength() {
        return this.data.length;
      };
      KanjiData.prototype.getBitsLength = function getBitsLength() {
        return KanjiData.getBitsLength(this.data.length);
      };
      KanjiData.prototype.write = function(bitBuffer) {
        let i;
        for (i = 0; i < this.data.length; i++) {
          let value = Utils.toSJIS(this.data[i]);
          if (value >= 33088 && value <= 40956) {
            value -= 33088;
          } else if (value >= 57408 && value <= 60351) {
            value -= 49472;
          } else {
            throw new Error(
              "Invalid SJIS character: " + this.data[i] + "\nMake sure your charset is UTF-8"
            );
          }
          value = (value >>> 8 & 255) * 192 + (value & 255);
          bitBuffer.put(value, 13);
        }
      };
      module.exports = KanjiData;
    }
  });

  // node_modules/dijkstrajs/dijkstra.js
  var require_dijkstra = __commonJS({
    "node_modules/dijkstrajs/dijkstra.js"(exports, module) {
      "use strict";
      var dijkstra = {
        single_source_shortest_paths: function(graph, s, d) {
          var predecessors = {};
          var costs = {};
          costs[s] = 0;
          var open = dijkstra.PriorityQueue.make();
          open.push(s, 0);
          var closest, u, v, cost_of_s_to_u, adjacent_nodes, cost_of_e, cost_of_s_to_u_plus_cost_of_e, cost_of_s_to_v, first_visit;
          while (!open.empty()) {
            closest = open.pop();
            u = closest.value;
            cost_of_s_to_u = closest.cost;
            adjacent_nodes = graph[u] || {};
            for (v in adjacent_nodes) {
              if (adjacent_nodes.hasOwnProperty(v)) {
                cost_of_e = adjacent_nodes[v];
                cost_of_s_to_u_plus_cost_of_e = cost_of_s_to_u + cost_of_e;
                cost_of_s_to_v = costs[v];
                first_visit = typeof costs[v] === "undefined";
                if (first_visit || cost_of_s_to_v > cost_of_s_to_u_plus_cost_of_e) {
                  costs[v] = cost_of_s_to_u_plus_cost_of_e;
                  open.push(v, cost_of_s_to_u_plus_cost_of_e);
                  predecessors[v] = u;
                }
              }
            }
          }
          if (typeof d !== "undefined" && typeof costs[d] === "undefined") {
            var msg = ["Could not find a path from ", s, " to ", d, "."].join("");
            throw new Error(msg);
          }
          return predecessors;
        },
        extract_shortest_path_from_predecessor_list: function(predecessors, d) {
          var nodes = [];
          var u = d;
          var predecessor;
          while (u) {
            nodes.push(u);
            predecessor = predecessors[u];
            u = predecessors[u];
          }
          nodes.reverse();
          return nodes;
        },
        find_path: function(graph, s, d) {
          var predecessors = dijkstra.single_source_shortest_paths(graph, s, d);
          return dijkstra.extract_shortest_path_from_predecessor_list(
            predecessors,
            d
          );
        },
        /**
         * A very naive priority queue implementation.
         */
        PriorityQueue: {
          make: function(opts) {
            var T = dijkstra.PriorityQueue, t = {}, key;
            opts = opts || {};
            for (key in T) {
              if (T.hasOwnProperty(key)) {
                t[key] = T[key];
              }
            }
            t.queue = [];
            t.sorter = opts.sorter || T.default_sorter;
            return t;
          },
          default_sorter: function(a, b) {
            return a.cost - b.cost;
          },
          /**
           * Add a new item to the queue and ensure the highest priority element
           * is at the front of the queue.
           */
          push: function(value, cost) {
            var item = { value, cost };
            this.queue.push(item);
            this.queue.sort(this.sorter);
          },
          /**
           * Return the highest priority element in the queue.
           */
          pop: function() {
            return this.queue.shift();
          },
          empty: function() {
            return this.queue.length === 0;
          }
        }
      };
      if (typeof module !== "undefined") {
        module.exports = dijkstra;
      }
    }
  });

  // node_modules/qrcode/lib/core/segments.js
  var require_segments = __commonJS({
    "node_modules/qrcode/lib/core/segments.js"(exports) {
      var Mode = require_mode();
      var NumericData = require_numeric_data();
      var AlphanumericData = require_alphanumeric_data();
      var ByteData = require_byte_data();
      var KanjiData = require_kanji_data();
      var Regex = require_regex();
      var Utils = require_utils();
      var dijkstra = require_dijkstra();
      function getStringByteLength(str) {
        return unescape(encodeURIComponent(str)).length;
      }
      function getSegments(regex, mode, str) {
        const segments = [];
        let result;
        while ((result = regex.exec(str)) !== null) {
          segments.push({
            data: result[0],
            index: result.index,
            mode,
            length: result[0].length
          });
        }
        return segments;
      }
      function getSegmentsFromString(dataStr) {
        const numSegs = getSegments(Regex.NUMERIC, Mode.NUMERIC, dataStr);
        const alphaNumSegs = getSegments(Regex.ALPHANUMERIC, Mode.ALPHANUMERIC, dataStr);
        let byteSegs;
        let kanjiSegs;
        if (Utils.isKanjiModeEnabled()) {
          byteSegs = getSegments(Regex.BYTE, Mode.BYTE, dataStr);
          kanjiSegs = getSegments(Regex.KANJI, Mode.KANJI, dataStr);
        } else {
          byteSegs = getSegments(Regex.BYTE_KANJI, Mode.BYTE, dataStr);
          kanjiSegs = [];
        }
        const segs = numSegs.concat(alphaNumSegs, byteSegs, kanjiSegs);
        return segs.sort(function(s1, s2) {
          return s1.index - s2.index;
        }).map(function(obj) {
          return {
            data: obj.data,
            mode: obj.mode,
            length: obj.length
          };
        });
      }
      function getSegmentBitsLength(length, mode) {
        switch (mode) {
          case Mode.NUMERIC:
            return NumericData.getBitsLength(length);
          case Mode.ALPHANUMERIC:
            return AlphanumericData.getBitsLength(length);
          case Mode.KANJI:
            return KanjiData.getBitsLength(length);
          case Mode.BYTE:
            return ByteData.getBitsLength(length);
        }
      }
      function mergeSegments(segs) {
        return segs.reduce(function(acc, curr) {
          const prevSeg = acc.length - 1 >= 0 ? acc[acc.length - 1] : null;
          if (prevSeg && prevSeg.mode === curr.mode) {
            acc[acc.length - 1].data += curr.data;
            return acc;
          }
          acc.push(curr);
          return acc;
        }, []);
      }
      function buildNodes(segs) {
        const nodes = [];
        for (let i = 0; i < segs.length; i++) {
          const seg = segs[i];
          switch (seg.mode) {
            case Mode.NUMERIC:
              nodes.push([
                seg,
                { data: seg.data, mode: Mode.ALPHANUMERIC, length: seg.length },
                { data: seg.data, mode: Mode.BYTE, length: seg.length }
              ]);
              break;
            case Mode.ALPHANUMERIC:
              nodes.push([
                seg,
                { data: seg.data, mode: Mode.BYTE, length: seg.length }
              ]);
              break;
            case Mode.KANJI:
              nodes.push([
                seg,
                { data: seg.data, mode: Mode.BYTE, length: getStringByteLength(seg.data) }
              ]);
              break;
            case Mode.BYTE:
              nodes.push([
                { data: seg.data, mode: Mode.BYTE, length: getStringByteLength(seg.data) }
              ]);
          }
        }
        return nodes;
      }
      function buildGraph(nodes, version) {
        const table = {};
        const graph = { start: {} };
        let prevNodeIds = ["start"];
        for (let i = 0; i < nodes.length; i++) {
          const nodeGroup = nodes[i];
          const currentNodeIds = [];
          for (let j = 0; j < nodeGroup.length; j++) {
            const node = nodeGroup[j];
            const key = "" + i + j;
            currentNodeIds.push(key);
            table[key] = { node, lastCount: 0 };
            graph[key] = {};
            for (let n = 0; n < prevNodeIds.length; n++) {
              const prevNodeId = prevNodeIds[n];
              if (table[prevNodeId] && table[prevNodeId].node.mode === node.mode) {
                graph[prevNodeId][key] = getSegmentBitsLength(table[prevNodeId].lastCount + node.length, node.mode) - getSegmentBitsLength(table[prevNodeId].lastCount, node.mode);
                table[prevNodeId].lastCount += node.length;
              } else {
                if (table[prevNodeId]) table[prevNodeId].lastCount = node.length;
                graph[prevNodeId][key] = getSegmentBitsLength(node.length, node.mode) + 4 + Mode.getCharCountIndicator(node.mode, version);
              }
            }
          }
          prevNodeIds = currentNodeIds;
        }
        for (let n = 0; n < prevNodeIds.length; n++) {
          graph[prevNodeIds[n]].end = 0;
        }
        return { map: graph, table };
      }
      function buildSingleSegment(data, modesHint) {
        let mode;
        const bestMode = Mode.getBestModeForData(data);
        mode = Mode.from(modesHint, bestMode);
        if (mode !== Mode.BYTE && mode.bit < bestMode.bit) {
          throw new Error('"' + data + '" cannot be encoded with mode ' + Mode.toString(mode) + ".\n Suggested mode is: " + Mode.toString(bestMode));
        }
        if (mode === Mode.KANJI && !Utils.isKanjiModeEnabled()) {
          mode = Mode.BYTE;
        }
        switch (mode) {
          case Mode.NUMERIC:
            return new NumericData(data);
          case Mode.ALPHANUMERIC:
            return new AlphanumericData(data);
          case Mode.KANJI:
            return new KanjiData(data);
          case Mode.BYTE:
            return new ByteData(data);
        }
      }
      exports.fromArray = function fromArray(array) {
        return array.reduce(function(acc, seg) {
          if (typeof seg === "string") {
            acc.push(buildSingleSegment(seg, null));
          } else if (seg.data) {
            acc.push(buildSingleSegment(seg.data, seg.mode));
          }
          return acc;
        }, []);
      };
      exports.fromString = function fromString(data, version) {
        const segs = getSegmentsFromString(data, Utils.isKanjiModeEnabled());
        const nodes = buildNodes(segs);
        const graph = buildGraph(nodes, version);
        const path = dijkstra.find_path(graph.map, "start", "end");
        const optimizedSegs = [];
        for (let i = 1; i < path.length - 1; i++) {
          optimizedSegs.push(graph.table[path[i]].node);
        }
        return exports.fromArray(mergeSegments(optimizedSegs));
      };
      exports.rawSplit = function rawSplit(data) {
        return exports.fromArray(
          getSegmentsFromString(data, Utils.isKanjiModeEnabled())
        );
      };
    }
  });

  // node_modules/qrcode/lib/core/qrcode.js
  var require_qrcode = __commonJS({
    "node_modules/qrcode/lib/core/qrcode.js"(exports) {
      var Utils = require_utils();
      var ECLevel = require_error_correction_level();
      var BitBuffer = require_bit_buffer();
      var BitMatrix = require_bit_matrix();
      var AlignmentPattern = require_alignment_pattern();
      var FinderPattern = require_finder_pattern();
      var MaskPattern = require_mask_pattern();
      var ECCode = require_error_correction_code();
      var ReedSolomonEncoder = require_reed_solomon_encoder();
      var Version = require_version();
      var FormatInfo = require_format_info();
      var Mode = require_mode();
      var Segments = require_segments();
      function setupFinderPattern(matrix, version) {
        const size = matrix.size;
        const pos = FinderPattern.getPositions(version);
        for (let i = 0; i < pos.length; i++) {
          const row = pos[i][0];
          const col = pos[i][1];
          for (let r = -1; r <= 7; r++) {
            if (row + r <= -1 || size <= row + r) continue;
            for (let c = -1; c <= 7; c++) {
              if (col + c <= -1 || size <= col + c) continue;
              if (r >= 0 && r <= 6 && (c === 0 || c === 6) || c >= 0 && c <= 6 && (r === 0 || r === 6) || r >= 2 && r <= 4 && c >= 2 && c <= 4) {
                matrix.set(row + r, col + c, true, true);
              } else {
                matrix.set(row + r, col + c, false, true);
              }
            }
          }
        }
      }
      function setupTimingPattern(matrix) {
        const size = matrix.size;
        for (let r = 8; r < size - 8; r++) {
          const value = r % 2 === 0;
          matrix.set(r, 6, value, true);
          matrix.set(6, r, value, true);
        }
      }
      function setupAlignmentPattern(matrix, version) {
        const pos = AlignmentPattern.getPositions(version);
        for (let i = 0; i < pos.length; i++) {
          const row = pos[i][0];
          const col = pos[i][1];
          for (let r = -2; r <= 2; r++) {
            for (let c = -2; c <= 2; c++) {
              if (r === -2 || r === 2 || c === -2 || c === 2 || r === 0 && c === 0) {
                matrix.set(row + r, col + c, true, true);
              } else {
                matrix.set(row + r, col + c, false, true);
              }
            }
          }
        }
      }
      function setupVersionInfo(matrix, version) {
        const size = matrix.size;
        const bits = Version.getEncodedBits(version);
        let row, col, mod;
        for (let i = 0; i < 18; i++) {
          row = Math.floor(i / 3);
          col = i % 3 + size - 8 - 3;
          mod = (bits >> i & 1) === 1;
          matrix.set(row, col, mod, true);
          matrix.set(col, row, mod, true);
        }
      }
      function setupFormatInfo(matrix, errorCorrectionLevel, maskPattern) {
        const size = matrix.size;
        const bits = FormatInfo.getEncodedBits(errorCorrectionLevel, maskPattern);
        let i, mod;
        for (i = 0; i < 15; i++) {
          mod = (bits >> i & 1) === 1;
          if (i < 6) {
            matrix.set(i, 8, mod, true);
          } else if (i < 8) {
            matrix.set(i + 1, 8, mod, true);
          } else {
            matrix.set(size - 15 + i, 8, mod, true);
          }
          if (i < 8) {
            matrix.set(8, size - i - 1, mod, true);
          } else if (i < 9) {
            matrix.set(8, 15 - i - 1 + 1, mod, true);
          } else {
            matrix.set(8, 15 - i - 1, mod, true);
          }
        }
        matrix.set(size - 8, 8, 1, true);
      }
      function setupData(matrix, data) {
        const size = matrix.size;
        let inc = -1;
        let row = size - 1;
        let bitIndex = 7;
        let byteIndex = 0;
        for (let col = size - 1; col > 0; col -= 2) {
          if (col === 6) col--;
          while (true) {
            for (let c = 0; c < 2; c++) {
              if (!matrix.isReserved(row, col - c)) {
                let dark = false;
                if (byteIndex < data.length) {
                  dark = (data[byteIndex] >>> bitIndex & 1) === 1;
                }
                matrix.set(row, col - c, dark);
                bitIndex--;
                if (bitIndex === -1) {
                  byteIndex++;
                  bitIndex = 7;
                }
              }
            }
            row += inc;
            if (row < 0 || size <= row) {
              row -= inc;
              inc = -inc;
              break;
            }
          }
        }
      }
      function createData(version, errorCorrectionLevel, segments) {
        const buffer = new BitBuffer();
        segments.forEach(function(data) {
          buffer.put(data.mode.bit, 4);
          buffer.put(data.getLength(), Mode.getCharCountIndicator(data.mode, version));
          data.write(buffer);
        });
        const totalCodewords = Utils.getSymbolTotalCodewords(version);
        const ecTotalCodewords = ECCode.getTotalCodewordsCount(version, errorCorrectionLevel);
        const dataTotalCodewordsBits = (totalCodewords - ecTotalCodewords) * 8;
        if (buffer.getLengthInBits() + 4 <= dataTotalCodewordsBits) {
          buffer.put(0, 4);
        }
        while (buffer.getLengthInBits() % 8 !== 0) {
          buffer.putBit(0);
        }
        const remainingByte = (dataTotalCodewordsBits - buffer.getLengthInBits()) / 8;
        for (let i = 0; i < remainingByte; i++) {
          buffer.put(i % 2 ? 17 : 236, 8);
        }
        return createCodewords(buffer, version, errorCorrectionLevel);
      }
      function createCodewords(bitBuffer, version, errorCorrectionLevel) {
        const totalCodewords = Utils.getSymbolTotalCodewords(version);
        const ecTotalCodewords = ECCode.getTotalCodewordsCount(version, errorCorrectionLevel);
        const dataTotalCodewords = totalCodewords - ecTotalCodewords;
        const ecTotalBlocks = ECCode.getBlocksCount(version, errorCorrectionLevel);
        const blocksInGroup2 = totalCodewords % ecTotalBlocks;
        const blocksInGroup1 = ecTotalBlocks - blocksInGroup2;
        const totalCodewordsInGroup1 = Math.floor(totalCodewords / ecTotalBlocks);
        const dataCodewordsInGroup1 = Math.floor(dataTotalCodewords / ecTotalBlocks);
        const dataCodewordsInGroup2 = dataCodewordsInGroup1 + 1;
        const ecCount = totalCodewordsInGroup1 - dataCodewordsInGroup1;
        const rs = new ReedSolomonEncoder(ecCount);
        let offset = 0;
        const dcData = new Array(ecTotalBlocks);
        const ecData = new Array(ecTotalBlocks);
        let maxDataSize = 0;
        const buffer = new Uint8Array(bitBuffer.buffer);
        for (let b = 0; b < ecTotalBlocks; b++) {
          const dataSize = b < blocksInGroup1 ? dataCodewordsInGroup1 : dataCodewordsInGroup2;
          dcData[b] = buffer.slice(offset, offset + dataSize);
          ecData[b] = rs.encode(dcData[b]);
          offset += dataSize;
          maxDataSize = Math.max(maxDataSize, dataSize);
        }
        const data = new Uint8Array(totalCodewords);
        let index = 0;
        let i, r;
        for (i = 0; i < maxDataSize; i++) {
          for (r = 0; r < ecTotalBlocks; r++) {
            if (i < dcData[r].length) {
              data[index++] = dcData[r][i];
            }
          }
        }
        for (i = 0; i < ecCount; i++) {
          for (r = 0; r < ecTotalBlocks; r++) {
            data[index++] = ecData[r][i];
          }
        }
        return data;
      }
      function createSymbol(data, version, errorCorrectionLevel, maskPattern) {
        let segments;
        if (Array.isArray(data)) {
          segments = Segments.fromArray(data);
        } else if (typeof data === "string") {
          let estimatedVersion = version;
          if (!estimatedVersion) {
            const rawSegments = Segments.rawSplit(data);
            estimatedVersion = Version.getBestVersionForData(rawSegments, errorCorrectionLevel);
          }
          segments = Segments.fromString(data, estimatedVersion || 40);
        } else {
          throw new Error("Invalid data");
        }
        const bestVersion = Version.getBestVersionForData(segments, errorCorrectionLevel);
        if (!bestVersion) {
          throw new Error("The amount of data is too big to be stored in a QR Code");
        }
        if (!version) {
          version = bestVersion;
        } else if (version < bestVersion) {
          throw new Error(
            "\nThe chosen QR Code version cannot contain this amount of data.\nMinimum version required to store current data is: " + bestVersion + ".\n"
          );
        }
        const dataBits = createData(version, errorCorrectionLevel, segments);
        const moduleCount = Utils.getSymbolSize(version);
        const modules = new BitMatrix(moduleCount);
        setupFinderPattern(modules, version);
        setupTimingPattern(modules);
        setupAlignmentPattern(modules, version);
        setupFormatInfo(modules, errorCorrectionLevel, 0);
        if (version >= 7) {
          setupVersionInfo(modules, version);
        }
        setupData(modules, dataBits);
        if (isNaN(maskPattern)) {
          maskPattern = MaskPattern.getBestMask(
            modules,
            setupFormatInfo.bind(null, modules, errorCorrectionLevel)
          );
        }
        MaskPattern.applyMask(maskPattern, modules);
        setupFormatInfo(modules, errorCorrectionLevel, maskPattern);
        return {
          modules,
          version,
          errorCorrectionLevel,
          maskPattern,
          segments
        };
      }
      exports.create = function create(data, options) {
        if (typeof data === "undefined" || data === "") {
          throw new Error("No input text");
        }
        let errorCorrectionLevel = ECLevel.M;
        let version;
        let mask;
        if (typeof options !== "undefined") {
          errorCorrectionLevel = ECLevel.from(options.errorCorrectionLevel, ECLevel.M);
          version = Version.from(options.version);
          mask = MaskPattern.from(options.maskPattern);
          if (options.toSJISFunc) {
            Utils.setToSJISFunction(options.toSJISFunc);
          }
        }
        return createSymbol(data, version, errorCorrectionLevel, mask);
      };
    }
  });

  // node_modules/qrcode/lib/renderer/utils.js
  var require_utils2 = __commonJS({
    "node_modules/qrcode/lib/renderer/utils.js"(exports) {
      function hex2rgba(hex) {
        if (typeof hex === "number") {
          hex = hex.toString();
        }
        if (typeof hex !== "string") {
          throw new Error("Color should be defined as hex string");
        }
        let hexCode = hex.slice().replace("#", "").split("");
        if (hexCode.length < 3 || hexCode.length === 5 || hexCode.length > 8) {
          throw new Error("Invalid hex color: " + hex);
        }
        if (hexCode.length === 3 || hexCode.length === 4) {
          hexCode = Array.prototype.concat.apply([], hexCode.map(function(c) {
            return [c, c];
          }));
        }
        if (hexCode.length === 6) hexCode.push("F", "F");
        const hexValue = parseInt(hexCode.join(""), 16);
        return {
          r: hexValue >> 24 & 255,
          g: hexValue >> 16 & 255,
          b: hexValue >> 8 & 255,
          a: hexValue & 255,
          hex: "#" + hexCode.slice(0, 6).join("")
        };
      }
      exports.getOptions = function getOptions(options) {
        if (!options) options = {};
        if (!options.color) options.color = {};
        const margin = typeof options.margin === "undefined" || options.margin === null || options.margin < 0 ? 4 : options.margin;
        const width = options.width && options.width >= 21 ? options.width : void 0;
        const scale = options.scale || 4;
        return {
          width,
          scale: width ? 4 : scale,
          margin,
          color: {
            dark: hex2rgba(options.color.dark || "#000000ff"),
            light: hex2rgba(options.color.light || "#ffffffff")
          },
          type: options.type,
          rendererOpts: options.rendererOpts || {}
        };
      };
      exports.getScale = function getScale(qrSize, opts) {
        return opts.width && opts.width >= qrSize + opts.margin * 2 ? opts.width / (qrSize + opts.margin * 2) : opts.scale;
      };
      exports.getImageWidth = function getImageWidth(qrSize, opts) {
        const scale = exports.getScale(qrSize, opts);
        return Math.floor((qrSize + opts.margin * 2) * scale);
      };
      exports.qrToImageData = function qrToImageData(imgData, qr, opts) {
        const size = qr.modules.size;
        const data = qr.modules.data;
        const scale = exports.getScale(size, opts);
        const symbolSize = Math.floor((size + opts.margin * 2) * scale);
        const scaledMargin = opts.margin * scale;
        const palette = [opts.color.light, opts.color.dark];
        for (let i = 0; i < symbolSize; i++) {
          for (let j = 0; j < symbolSize; j++) {
            let posDst = (i * symbolSize + j) * 4;
            let pxColor = opts.color.light;
            if (i >= scaledMargin && j >= scaledMargin && i < symbolSize - scaledMargin && j < symbolSize - scaledMargin) {
              const iSrc = Math.floor((i - scaledMargin) / scale);
              const jSrc = Math.floor((j - scaledMargin) / scale);
              pxColor = palette[data[iSrc * size + jSrc] ? 1 : 0];
            }
            imgData[posDst++] = pxColor.r;
            imgData[posDst++] = pxColor.g;
            imgData[posDst++] = pxColor.b;
            imgData[posDst] = pxColor.a;
          }
        }
      };
    }
  });

  // node_modules/qrcode/lib/renderer/canvas.js
  var require_canvas = __commonJS({
    "node_modules/qrcode/lib/renderer/canvas.js"(exports) {
      var Utils = require_utils2();
      function clearCanvas(ctx, canvas, size) {
        ctx.clearRect(0, 0, canvas.width, canvas.height);
        if (!canvas.style) canvas.style = {};
        canvas.height = size;
        canvas.width = size;
        canvas.style.height = size + "px";
        canvas.style.width = size + "px";
      }
      function getCanvasElement() {
        try {
          return document.createElement("canvas");
        } catch (e) {
          throw new Error("You need to specify a canvas element");
        }
      }
      exports.render = function render(qrData, canvas, options) {
        let opts = options;
        let canvasEl = canvas;
        if (typeof opts === "undefined" && (!canvas || !canvas.getContext)) {
          opts = canvas;
          canvas = void 0;
        }
        if (!canvas) {
          canvasEl = getCanvasElement();
        }
        opts = Utils.getOptions(opts);
        const size = Utils.getImageWidth(qrData.modules.size, opts);
        const ctx = canvasEl.getContext("2d");
        const image = ctx.createImageData(size, size);
        Utils.qrToImageData(image.data, qrData, opts);
        clearCanvas(ctx, canvasEl, size);
        ctx.putImageData(image, 0, 0);
        return canvasEl;
      };
      exports.renderToDataURL = function renderToDataURL(qrData, canvas, options) {
        let opts = options;
        if (typeof opts === "undefined" && (!canvas || !canvas.getContext)) {
          opts = canvas;
          canvas = void 0;
        }
        if (!opts) opts = {};
        const canvasEl = exports.render(qrData, canvas, opts);
        const type = opts.type || "image/png";
        const rendererOpts = opts.rendererOpts || {};
        return canvasEl.toDataURL(type, rendererOpts.quality);
      };
    }
  });

  // node_modules/qrcode/lib/renderer/svg-tag.js
  var require_svg_tag = __commonJS({
    "node_modules/qrcode/lib/renderer/svg-tag.js"(exports) {
      var Utils = require_utils2();
      function getColorAttrib(color, attrib) {
        const alpha = color.a / 255;
        const str = attrib + '="' + color.hex + '"';
        return alpha < 1 ? str + " " + attrib + '-opacity="' + alpha.toFixed(2).slice(1) + '"' : str;
      }
      function svgCmd(cmd, x, y) {
        let str = cmd + x;
        if (typeof y !== "undefined") str += " " + y;
        return str;
      }
      function qrToPath(data, size, margin) {
        let path = "";
        let moveBy = 0;
        let newRow = false;
        let lineLength = 0;
        for (let i = 0; i < data.length; i++) {
          const col = Math.floor(i % size);
          const row = Math.floor(i / size);
          if (!col && !newRow) newRow = true;
          if (data[i]) {
            lineLength++;
            if (!(i > 0 && col > 0 && data[i - 1])) {
              path += newRow ? svgCmd("M", col + margin, 0.5 + row + margin) : svgCmd("m", moveBy, 0);
              moveBy = 0;
              newRow = false;
            }
            if (!(col + 1 < size && data[i + 1])) {
              path += svgCmd("h", lineLength);
              lineLength = 0;
            }
          } else {
            moveBy++;
          }
        }
        return path;
      }
      exports.render = function render(qrData, options, cb) {
        const opts = Utils.getOptions(options);
        const size = qrData.modules.size;
        const data = qrData.modules.data;
        const qrcodesize = size + opts.margin * 2;
        const bg = !opts.color.light.a ? "" : "<path " + getColorAttrib(opts.color.light, "fill") + ' d="M0 0h' + qrcodesize + "v" + qrcodesize + 'H0z"/>';
        const path = "<path " + getColorAttrib(opts.color.dark, "stroke") + ' d="' + qrToPath(data, size, opts.margin) + '"/>';
        const viewBox = 'viewBox="0 0 ' + qrcodesize + " " + qrcodesize + '"';
        const width = !opts.width ? "" : 'width="' + opts.width + '" height="' + opts.width + '" ';
        const svgTag = '<svg xmlns="http://www.w3.org/2000/svg" ' + width + viewBox + ' shape-rendering="crispEdges">' + bg + path + "</svg>\n";
        if (typeof cb === "function") {
          cb(null, svgTag);
        }
        return svgTag;
      };
    }
  });

  // node_modules/qrcode/lib/browser.js
  var require_browser = __commonJS({
    "node_modules/qrcode/lib/browser.js"(exports) {
      var canPromise = require_can_promise();
      var QRCode2 = require_qrcode();
      var CanvasRenderer = require_canvas();
      var SvgRenderer = require_svg_tag();
      function renderCanvas(renderFunc, canvas, text, opts, cb) {
        const args = [].slice.call(arguments, 1);
        const argsNum = args.length;
        const isLastArgCb = typeof args[argsNum - 1] === "function";
        if (!isLastArgCb && !canPromise()) {
          throw new Error("Callback required as last argument");
        }
        if (isLastArgCb) {
          if (argsNum < 2) {
            throw new Error("Too few arguments provided");
          }
          if (argsNum === 2) {
            cb = text;
            text = canvas;
            canvas = opts = void 0;
          } else if (argsNum === 3) {
            if (canvas.getContext && typeof cb === "undefined") {
              cb = opts;
              opts = void 0;
            } else {
              cb = opts;
              opts = text;
              text = canvas;
              canvas = void 0;
            }
          }
        } else {
          if (argsNum < 1) {
            throw new Error("Too few arguments provided");
          }
          if (argsNum === 1) {
            text = canvas;
            canvas = opts = void 0;
          } else if (argsNum === 2 && !canvas.getContext) {
            opts = text;
            text = canvas;
            canvas = void 0;
          }
          return new Promise(function(resolve, reject) {
            try {
              const data = QRCode2.create(text, opts);
              resolve(renderFunc(data, canvas, opts));
            } catch (e) {
              reject(e);
            }
          });
        }
        try {
          const data = QRCode2.create(text, opts);
          cb(null, renderFunc(data, canvas, opts));
        } catch (e) {
          cb(e);
        }
      }
      exports.create = QRCode2.create;
      exports.toCanvas = renderCanvas.bind(null, CanvasRenderer.render);
      exports.toDataURL = renderCanvas.bind(null, CanvasRenderer.renderToDataURL);
      exports.toString = renderCanvas.bind(null, function(data, _, opts) {
        return SvgRenderer.render(data, opts);
      });
    }
  });

  // renderer/takeout.ts
  var import_qrcode = __toESM(require_browser());

  // renderer/figureTrim.ts
  var ALPHA_MIN = 8;
  var MAX_SAMPLE = 2048;
  var IGNORE = 8e-3;
  var MIN_CONTENT = 0.25;
  var MAX_SCALE = 4;
  var NONE = { left: 0, right: 0, top: 0, bottom: 0, content: 1, scale: 1, transform: "none" };
  function norm(v) {
    return v < IGNORE ? 0 : v;
  }
  function measureFigureTrim(img) {
    const w = img.naturalWidth;
    const h = img.naturalHeight;
    if (!w || !h) return NONE;
    const s = Math.min(1, MAX_SAMPLE / Math.max(w, h));
    const cw = Math.max(1, Math.round(w * s));
    const ch = Math.max(1, Math.round(h * s));
    const cv = document.createElement("canvas");
    cv.width = cw;
    cv.height = ch;
    const ctx = cv.getContext("2d", { willReadFrequently: true });
    if (!ctx) return NONE;
    ctx.clearRect(0, 0, cw, ch);
    ctx.drawImage(img, 0, 0, cw, ch);
    let px;
    try {
      px = ctx.getImageData(0, 0, cw, ch).data;
    } catch {
      return NONE;
    }
    let top = -1;
    let bottom = -1;
    let left = cw;
    let right = -1;
    for (let y = 0; y < ch; y++) {
      const row = y * cw * 4;
      let has = false;
      for (let x = 0; x < cw; x++) {
        if (px[row + x * 4 + 3] >= ALPHA_MIN) {
          has = true;
          if (x < left) left = x;
          if (x > right) right = x;
        }
      }
      if (has) {
        if (top < 0) top = y;
        bottom = y;
      }
    }
    if (bottom < 0 || right < 0) return NONE;
    const out = {
      left: norm(left / cw),
      right: norm((cw - 1 - right) / cw),
      top: norm(top / ch),
      bottom: norm((ch - 1 - bottom) / ch),
      // 未过 IGNORE 阈值的原始边距（摆位锚点专用，与 break.js 同一套）：
      // IGNORE 会把 <0.8% 的边距归零，对变换无害，但锚点必须指向像素真实位置。
      rawLeft: left / cw,
      rawRight: (cw - 1 - right) / cw,
      rawTop: top / ch,
      rawBottom: (ch - 1 - bottom) / ch,
      content: 1,
      scale: 1,
      transform: "none"
    };
    out.content = 1 - out.top - out.bottom;
    if (out.content < MIN_CONTENT) return NONE;
    return out;
  }
  var measured = /* @__PURE__ */ new WeakMap();
  function applyFigureTrim(host, img, flip = false) {
    host.style.transformOrigin = "left bottom";
    const run = () => {
      host.style.transform = "none";
      const t = measureFigureTrim(img);
      const left = flip ? t.right : t.left;
      const right = flip ? t.left : t.right;
      // ⚠️ fill 必须按**布局盒**算，不能读 img 的 getBoundingClientRect：img 自身
      // 可能带着 Stage 变换（#app[data-stage='1'] 的 scale(1.03) scaleY(1.006)，
      // 纵向 1.03618 > 横向 1.03）。立绘图常在变换生效之后才加载完，那时量到的是
      // 变换后尺寸——W/H 比自然 aspect 小 0.6%，fill 被压成 0.994（应为 1.0），
      // trim 的平移量整体偏小、立绘可见边与红包锚点一起算歪（实测顶缘差 ~4px）。
      // host 的变换已在函数开头重置为 none，其 rect 即布局盒（img 盒撑满 host），
      // 且不受 img 自身变换影响。
      const rendered = host.getBoundingClientRect();
      const scale = t.content >= MIN_CONTENT ? Math.min(MAX_SCALE, 1 / t.content) : 1;
      const ar = img.naturalHeight > 0 ? img.naturalWidth / img.naturalHeight : 0;
      const fill = rendered.height > 0 && ar > 0 ? Math.min(1, rendered.width / ar / rendered.height) : 1;
      const needTrim = left > 0 || t.top > 0 || t.bottom > 0;
      const transform = needTrim ? `translate(${(-left * scale * 100).toFixed(3)}%, ${(t.bottom * scale * fill * 100).toFixed(3)}%) scale(${scale.toFixed(4)})` : "none";
      host.style.transform = transform;
      host.style.visibility = "visible";
      img.dataset.trim = JSON.stringify({
        left: +left.toFixed(4),
        right: +right.toFixed(4),
        top: +t.top.toFixed(4),
        bottom: +t.bottom.toFixed(4),
        // 像素真实边距（未过 IGNORE）：摆位锚点用，语义与 left/right 一致（含 flip 交换）
        rawLeft: +(flip ? t.rawRight : t.rawLeft).toFixed(4),
        rawRight: +(flip ? t.rawLeft : t.rawRight).toFixed(4),
        rawTop: +t.rawTop.toFixed(4),
        rawBottom: +t.rawBottom.toFixed(4),
        scale: +scale.toFixed(4),
        fill: +fill.toFixed(4),
        flip,
        transform
      });
    };
    const cacheKey = `${img.src}|${flip ? "F" : "-"}`;
    if (measured.get(img) === cacheKey && img.dataset.trim) {
      const cached = JSON.parse(img.dataset.trim);
      host.style.transform = cached.transform;
      host.style.visibility = "visible";
      return;
    }
    measured.set(img, cacheKey);
    if (img.complete && img.naturalWidth > 0) {
      run();
      return;
    }
    host.style.visibility = "hidden";
    img.addEventListener("load", run, { once: true });
    img.addEventListener("error", () => {
      img.dataset.trim = JSON.stringify({ transform: "none", flip });
      host.style.visibility = "visible";
    }, { once: true });
  }

  // renderer/supervisorStage.ts（外卖窗口版）
  // 与休息窗口同源，但**没有二阶段**：外卖提醒不玩“凑近命令”——督休专员台词
  // 恒为外卖提醒文字（stage1Text），不显示督休专员自己的劝说/命令台词；
  // 检测到活动只向其他窗口广播升级（supActivity），本窗口不升级。
  var GEOM_FALLBACK = { sizePct: 80, posXPct: 5, posYPct: 0, flip: false };
  function setupSupervisor(sup, stage1Text) {
    const app = document.getElementById("app");
    const figure = document.getElementById("figure");
    const gaze = document.getElementById("gaze");
    const img = document.getElementById("figure-img");
    const line = document.getElementById("supervisor-line");
    document.body.classList.add("has-supervisor");
    figure.classList.remove("hidden");
    if (sup.name) line.dataset.speaker = sup.name;
    else delete line.dataset.speaker;
    // 督休专员题名：竖排金色宋体，钉在立绘头部左侧的空白区（.dialog-name）。
    // 位置与字号由 layoutRoleTitle() 按 alpha 剖面实测写入；这里只填文本，并清掉
    // 上一轮的 data-rt —— 换专员后旧位置对新立绘是错的，重新定位成功前保持不可见。
    const nameEl = document.getElementById("dialog-name");
    if (nameEl) {
      delete nameEl.dataset.rt;
      // 每次出场换一枚“章”：牌底轮廓/断裂与四周碎点一起换（同一下标）
      pickSealInk(nameEl);
      if (sup.name) {
        nameEl.textContent = sup.name;
        nameEl.classList.remove("hidden");
      } else {
        nameEl.textContent = "";
        nameEl.classList.add("hidden");
      }
    }
    const g = sup.geom ?? GEOM_FALLBACK;
    const flip = g.flip === true;
    document.body.classList.toggle("sup-flip", flip);
    const vars = document.documentElement.style;
    vars.setProperty("--fig-size", String(g.sizePct));
    vars.setProperty("--fig-x", String(g.posXPct));
    vars.setProperty("--fig-y", String(g.posYPct));
    img.src = sup.image;
    applyFigureTrim(document.getElementById("figure-trim"), img, flip);
    const supApi = window.api;
    // 台词 = 外卖提醒文字（不回退督休专员台词；没配提醒文字就不显示台词）
    const stage1 = stage1Text || "";
    app.dataset.stage = "1";
    line.textContent = stage1;
    if (!stage1) line.classList.add("hidden");
    else line.classList.remove("hidden");
    const broadcast = () => {
      supApi?.supActivity?.();
    };
    let lastMove = 0;
    window.addEventListener("mousemove", () => {
      const now = performance.now();
      if (now - lastMove < 500) return;
      lastMove = now;
      broadcast();
    });
    window.addEventListener("mousedown", broadcast, { passive: true });
    window.addEventListener("keydown", broadcast, { passive: true });
    window.addEventListener("wheel", broadcast, { passive: true });
    window.addEventListener("touchstart", broadcast, { passive: true });
    if (!sup.gaze) return;
    gaze.style.transformOrigin = "50% 100%";
    let targetNX = 0;
    let targetNY = 0;
    let curNX = 0;
    let curNY = 0;
    window.addEventListener("mousemove", (e) => {
      targetNX = e.clientX / window.innerWidth * 2 - 1;
      targetNY = e.clientY / window.innerHeight * 2 - 1;
    });
    // 题名牌要跟着立绘一起动，但它不能搬进 .gaze 那条 DOM 链：.trim 的补偿缩放
    // （立绘可见区越窄放得越大，1.0~2.0）会把牌面文字一起放大，而牌面位置是按
    // “渲染后的像素”反解出来的，进去就全错。所以把同一套变换镜像一份写过去：
    // 同一个原点（立绘盒底边中点 = 双脚，见 layoutRoleTitle 写的 --rt-ox/--rt-oy）、
    // 同一组函数、同一顺序 ⇒ 数学上等价于“牌面焊在立绘上”：立绘怎么偏，牌面就怎么偏，
    // 中间那条缝一个像素都不变。
    // 唯一要换算的是平移量 —— .gaze 的位移发生在 .trim 的缩放空间里，渲染出来是 scale
    // 倍，所以镜像时乘上它（skew 的角度、scaleY 的比值都不受均匀缩放影响）。
    let trimKey = "";
    let trimScale = 1;
    const trimScaleNow = () => {
      const raw = img.dataset.trim || "";
      if (raw !== trimKey) {
        trimKey = raw;
        try { trimScale = JSON.parse(raw).scale || 1; } catch (e) { trimScale = 1; }
      }
      return trimScale;
    };
    // 帧内只在字符串真的变了才写：插值收敛后每帧算出来是同一个值，
    // 无脑赋值会让 Chromium 每帧重算样式 + 重绘整块牌面（它有 drop-shadow 与
    // background-clip: text，重绘不便宜）。
    let lastGazeTf = "";
    let lastPlateTf = "";
    const frame = () => {
      curNX += (targetNX - curNX) * 0.075;
      curNY += (targetNY - curNY) * 0.075;
      const h = Math.max(1, gaze.offsetHeight);
      const tx = curNX * 14;
      const ty = Math.max(0, curNY) * 8;
      const lean = -curNX * 0.9;
      const sy = 1 + Math.max(0, -curNY) * 8 / h;
      const gtf = `translate3d(${tx.toFixed(2)}px, ${ty.toFixed(2)}px, 0) skewX(${lean.toFixed(2)}deg) scaleY(${sy.toFixed(4)})`;
      if (gtf !== lastGazeTf) { lastGazeTf = gtf; gaze.style.transform = gtf; }
      if (nameEl) {
        const k = trimScaleNow();
        const ptf = `translate3d(${(tx * k).toFixed(2)}px, ${(ty * k).toFixed(2)}px, 0) skewX(${lean.toFixed(2)}deg) scaleY(${sy.toFixed(4)})`;
        if (ptf !== lastPlateTf) { lastPlateTf = ptf; nameEl.style.transform = ptf; }
      }
      requestAnimationFrame(frame);
    };
    requestAnimationFrame(frame);
  }

  // renderer/supervisorLayout.ts —— 游戏对话画面版式（与 break.js 同一套）
  // .content 已是底部全宽对话框（CSS 定版），这里只负责把支付红包区浮到
  // “立绘可见右缘~窗口右缘”与“立绘可见上缘~窗口上边缘”两个中点的交点上
  // （红包中心对准）。摆位结果写入 supMetrics 供探针断言。
  var CD_EDGE = 10;
  function clamp(v, lo, hi) {
    return Math.min(Math.max(v, lo), hi);
  }
  // 立绘“去掉透明部分后”的可见边缘（窗口坐标）——与 break.js 同一套几何。
  // .trim 补偿变换 translate(-left*s%, bottom*s*fill%) scale(s)（origin: left bottom）
  // 把 img 盒（铺满立绘盒，盒左缘 x0、底缘 yB、盒宽 W、盒高 H）映射到屏幕，
  // 像素内容的四条可见边：
  //   · 左缘 = x0 + s*W*(rawLeft - left)
  //   · 右缘 = x0 + s*W*(1 - rawRight - left)
  //   · 顶缘 = yB - s*fill*H*(1 - rawTop - bottom)
  //   · 底缘 = yB - s*fill*H*(rawBottom - bottom)
  // left/bottom 是变换实际使用的边距（过 IGNORE，可能被归零），rawLeft/rawTop 等是
  // 像素真实边距（未归零）——二者只在边距 <0.8% 时不同。
  // ⚠️ 曾经的 bug：① 顶缘漏减 bottom；② 左右缘取含透明边距的变换后外框；
  // ③ fill 读 img 变换后的 rect（Stage 纵向缩放更大 → 被压成 0.994）。
  // trim 尚未就绪（图片未加载/量失败）时退化为立绘自身外框。
  function figureVisibleRect(figure, fallbackTop) {
    const box = figure.getBoundingClientRect();
    const img = figure.querySelector("img");
    let t = null;
    try { t = img ? JSON.parse(img.dataset.trim || "null") : null; } catch (e) { t = null; }
    if (!t || typeof t.scale !== "number" || box.width <= 0 || box.height <= 0) {
      return { left: box.left, right: box.right, top: fallbackTop, bottom: box.bottom };
    }
    const s = t.scale;
    const fill = typeof t.fill === "number" ? t.fill : 1;
    const l = typeof t.left === "number" ? t.left : 0;
    const bt = typeof t.bottom === "number" ? t.bottom : 0;
    const rl = typeof t.rawLeft === "number" ? t.rawLeft : l;
    const rr = typeof t.rawRight === "number" ? t.rawRight : (typeof t.right === "number" ? t.right : 0);
    const rtp = typeof t.rawTop === "number" ? t.rawTop : (typeof t.top === "number" ? t.top : 0);
    const rbt = typeof t.rawBottom === "number" ? t.rawBottom : bt;
    return {
      left: box.left + s * box.width * (rl - l),
      right: box.left + s * box.width * (1 - rr - l),
      top: box.bottom - s * fill * box.height * (1 - rtp - bt),
      bottom: box.bottom - s * fill * box.height * (rbt - bt)
    };
  }
  // ── 督休专员题名（竖排金色宋体）─────────────────────────────────────────
  // 立绘的“alpha 逐列剖面”。包围盒在这里不够用：立绘底部常有刀鞘/衣袍向左探出
  // （始皇帝包围盒左缘 44px，而头部那一段的最左不透明列在 148px），照包围盒摆名字
  // 会一头撞在人物身上。所以单独量一份 minY[x]（每列最上面的不透明像素行号）。
  // “头部段”= 图片顶部这一段。不能只看头顶那 16%：题名是竖排的，长一点的会向下
  // 伸到下颌/肩线附近，那里的内容（散落的发丝、肩甲）比头顶更靠左。按 16% 定的右缘
  // 到下面就会贴上去（竖屏“温柔学姐”实测 hit=1）。26% 能盖住题名的实际下探深度。
  var HEAD_BAND = 0.26;
  var PROFILE_W = 360;    // 剖面采样宽度：只用来找列，不需要高清
  // 题名避让用**实质遮挡**阈值：ALPHA_MIN(8) 太敏感，会把发丝边缘的抗锯齿（≈5%
  // 不透明度，压上去肉眼无感）也算成“有人”，于是把字号一路压到下限。32/255≈12.5%。
  var PROFILE_ALPHA = 32;
  var profileCache = new WeakMap();

  function measureHeadProfile(img) {
    // complete 必须一起判：dataURL/webp 在“数据到齐但还没解码”时 naturalWidth 就
    // 已经有值了，此时 drawImage 画出来是空的或残缺的，会量出一个假剖面。
    if (!img || !img.complete || !img.naturalWidth || !img.naturalHeight) return null;
    const hit = profileCache.get(img);
    if (hit && hit.src === img.src) return hit.profile;
    const w = img.naturalWidth;
    const h = img.naturalHeight;
    const cw = Math.min(PROFILE_W, w);
    const ch = Math.max(1, Math.round((h * cw) / w));
    const cv = document.createElement("canvas");
    cv.width = cw;
    cv.height = ch;
    const ctx = cv.getContext("2d", { willReadFrequently: true });
    if (!ctx) return null;
    ctx.clearRect(0, 0, cw, ch);
    ctx.drawImage(img, 0, 0, cw, ch);
    let px;
    try {
      px = ctx.getImageData(0, 0, cw, ch).data;
    } catch (e) {
      return null;   // 跨域/解码失败：题名退回不显示，不影响主流程
    }
    // minY[x] = 该列最上面的不透明像素行号；整列全空则保持 ch
    const minY = new Int32Array(cw).fill(ch);
    for (let x = 0; x < cw; x++) {
      for (let y = 0; y < ch; y++) {
        if (px[(y * cw + x) * 4 + 3] >= PROFILE_ALPHA) {
          minY[x] = y;
          break;
        }
      }
    }
    // 头部段内的最左 / 最右不透明列
    const bandEnd = Math.round(ch * HEAD_BAND);
    let headLeft = -1;
    let headRight = -1;
    for (let x = 0; x < cw; x++) {
      if (minY[x] <= bandEnd) {
        if (headLeft < 0) headLeft = x;
        headRight = x;
      }
    }
    const profile = { w: cw, h: ch, minY, headLeft, headRight };
    // 头部段整列为空时**绝不缓存**：正常立绘不会这样，出现它基本都是这次采样赶上了
    // 半解码。缓存下来的话后续重试永远拿到这份空剖面，名字就彻底不显示了。
    if (headLeft >= 0) profileCache.set(img, { src: img.src, profile });
    return profile;
  }

  var RT_GAP = 12;      // 题名右缘距“头部段最左列”的留白
  var RT_EDGE = 6;      // 题名离窗口左缘的最小留白
  var RT_MIN_FS = 15;   // 字号下限：1x 屏 14px 已发虚，15 是能读的底线
  var RT_MAX_FS = 26;
  var RT_STEP = 0.5;

  // 印泥变体：同一枚章连盖两次也不该一模一样。每次出场随机挑一组（牌子 + 周围碎点
  // 用同一个下标，保证“牌子和溅出来的碎点属于同一枚章”），并避开上一次用过的那张。
  // 变体由 scripts/make_seal_ink.py 生成，CSS 里的 url("seal-ink-1.svg") 只是兜底。
  var SEAL_VARIANTS = 6;
  var lastSeal = 0;
  function pickSealInk(nameEl) {
    if (!nameEl) return;
    // SEAL_VARIANTS 是 var（有声明提升但赋值在后面）：万一首帧就调用，退回 6
    var i = 1 + Math.floor(Math.random() * (SEAL_VARIANTS || 6));
    if (i === lastSeal) i = (i % SEAL_VARIANTS) + 1;   // 不连着用同一张
    lastSeal = i;
    nameEl.style.setProperty("--rt-ink", 'url("seal-ink-' + i + '.svg")');
    nameEl.style.setProperty("--rt-specks", 'url("seal-specks-' + i + '.svg")');
  }

  // 把题名钉到“立绘头部左侧的空白区”，并按零遮挡反解字号。
  // 只有先钉住右缘（= headPx - RT_GAP）再一路缩字号，才能同时保证“不压人”和
  // “不出窗”——固定字号再找位置，长名字必然二选一失败。
  function layoutRoleTitle(figure, nameEl, box) {
    if (!figure || !nameEl || nameEl.classList.contains("hidden")) return null;
    if (!nameEl.textContent) return null;
    if (!box || box.width <= 0 || box.height <= 0) return null;
    const img = figure.querySelector("img");
    const pf = measureHeadProfile(img);
    if (!pf || pf.headLeft < 0) return null;
    let t = null;
    try { t = JSON.parse(img.dataset.trim || "null"); } catch (e) { t = null; }
    const s = t && typeof t.scale === "number" ? t.scale : 1;
    const fill = t && typeof t.fill === "number" ? t.fill : 1;
    const l = t && typeof t.left === "number" ? t.left : 0;
    const rawTop = t && typeof t.rawTop === "number" ? t.rawTop : 0;
    const flip = !!(t && t.flip);

    // 与 figureVisibleRect() 同一套映射（见该函数上方注释），这里只是需要
    // “图片绘制区在页面里的绝对位置”，好把剖面坐标反算回去。
    const drawW = s * box.width;
    const drawH = s * fill * box.height;
    const imgLeft = box.left - drawW * l;
    const imgTop = box.bottom - drawH;
    const visTop = imgTop + drawH * rawTop;

    // 头部段最左列；水平翻转时头部镜像，要取原图的最右列
    const bandX = flip ? pf.w - 1 - pf.headRight : pf.headLeft;
    if (bandX < 0) return null;
    const headPx = imgLeft + (bandX / pf.w) * drawW;
    const rightPx = headPx - RT_GAP;

    const appEl = document.getElementById("app") || document.body;
    const appRect = appEl.getBoundingClientRect();

    // 零遮挡求解：从 RT_MAX_FS 起每档 −0.5px，直到“一个不透明像素都不压 +
    // 整块没被 RT_EDGE 钳住”。capH > 0 时给整块一个最大高度 —— vertical-rl
    // 据此折出第二列（右→左，中文竖排的读序），长名就能留在头部左侧的同一片
    // 空白里，不必一路缩到发虚。capH 生效时同时限制列数（maxCols）：实测 13 字名
    // 在 26px 会折成 3 列 98px 宽的金字块，压过头顶、读起来像“一块招牌”；
    // 限 2 列后整块是 40px 上下的一条窄带，与 3~4 字名的观感才连得上。
    // 注意 order：每次都要先把 left/top 钉回 0 再量，shrink-to-fit 元素否则
    // 量到的是上一轮的字号（74 当 35 那种）。
    // 题名现在带红牌（padding 来自 CSS），所以要先用“不折列”量出一列的自然尺寸，
    // 再自己算列数与宽度：Chromium 的 shrink-to-fit 在竖排 + max-height 下**只按
    // 一列算宽**，溢出列会整列漏到红底外面（实测 13 字名有 6 个字露在牌外）。
    const cs0 = getComputedStyle(nameEl);
    const padX = parseFloat(cs0.paddingLeft) || 0;
    const padY = parseFloat(cs0.paddingTop) || 0;
    const nChars = [...(nameEl.textContent || "")].length;
    function solve(capH, maxCols) {
      let f = RT_MAX_FS, l = 0, hit = 0, ok = false, cw = 0;
      for (; f >= RT_MIN_FS - 1e-6; f -= RT_STEP) {
        nameEl.style.setProperty("--rt-fs", f + "px");
        nameEl.style.left = "0px";
        nameEl.style.top = "0px";
        // ① 不折列，量一列的自然宽高（都是内容盒尺寸）
        nameEl.style.maxHeight = "none";
        nameEl.style.width = "auto";
        void nameEl.offsetWidth;
        const oneW = Math.max(1, nameEl.offsetWidth - padX * 2);
        const oneH = Math.max(1, nameEl.offsetHeight - padY * 2);
        // ② 需要折列就把宽度写死 = 列数 × 一列推进量
        let wpx, hpx, fits = true;
        cw = 0;
        if (capH > 0) {
          const innerH = Math.max(1, capH - padY * 2);
          // 列数不能拿“总高 / 段高”折算：每列只排得下整数个字。必须先用
          // 单字推进量（oneH / 字数）算出每列容量，再算需要几列 —— 否则 13 字在
          // 21px 会算成 2 列，实际要 3 列，整整一列漏到牌外（实测 scrollWidth 89 > 68）。
          const adv = oneH / Math.max(1, nChars);
          const perCol = Math.max(1, Math.floor(innerH / adv + 1e-6));
          const need = Math.max(1, Math.ceil(nChars / perCol));
          fits = need <= maxCols;                       // 列数不够 = 这个字号装不下
          // 宽度一律按**实际需要的列数**给，宁可宽也不漏字；全局 box-sizing:border-box，
          // 写 width 是含 padding 的宽（少加一次 padding 就会挤窄内容盒、多折一列）
          cw = Math.ceil(oneW * need) + padX * 2;
          nameEl.style.maxHeight = Math.round(capH) + "px";
          nameEl.style.width = cw + "px";
          void nameEl.offsetWidth;
          wpx = nameEl.offsetWidth;
          hpx = nameEl.offsetHeight;
        } else {
          wpx = oneW + padX * 2;
          hpx = oneH + padY * 2;
        }
        if (!wpx || !hpx) break;
        l = Math.max(RT_EDGE, rightPx - wpx);
        // 题名矩形 → 剖面列区间；数“该区间内比题名底更靠上的不透明像素”
        // ⚠️ minY 是按**原图列序**量的（canvas 直接 drawImage，没有翻转），而
        // imgLeft/drawW 是**显示方向**的映射。立绘水平翻转时（flip），屏幕 x 增大
        // = 原图列减小，列区间必须镜像 —— 漏掉这一步就去查了图片的另半边：牌面
        // 明明落在人物左侧的空白里，判据却在右侧对称位置数到一堆像素，于是“每列
        // 都算遮挡”（实测魔法师 hit=34，正好等于区间列数 34；字号被一路压到 15px
        // 下限，而实拍牌面底下是纯背景）。bandX 早就按 flip 取过头部左缘，这里
        // 是同一件事的另一半，漏了就成了两套坐标系混用。
        let cx0 = Math.max(0, Math.floor(((l - imgLeft) / drawW) * pf.w));
        let cx1 = Math.min(pf.w - 1, Math.ceil(((rightPx - imgLeft) / drawW) * pf.w));
        if (flip) {
          const mirrored = pf.w - 1 - cx1;
          cx1 = pf.w - 1 - cx0;
          cx0 = Math.max(0, mirrored);
        }
        const cy = ((visTop + hpx - imgTop) / drawH) * pf.h;
        hit = 0;
        for (let x = cx0; x <= cx1; x++) {
          if (pf.minY[x] < cy) hit++;
        }
        if (fits && !hit && rightPx - wpx >= RT_EDGE) { ok = true; break; }
      }
      return { fs: Math.max(RT_MIN_FS, f), left: l, hits: hit, ok, cap: capH, cw };
    }

    // 先按单列解（短名的常态，绝不能给它折列）。只有单列撑不住时才允许折 2 列重解：
    //   · 单列在下限字号都压到人（实测 12 字的“图书馆角落的温柔学姐”15px 仍 hit=7）
    //   · 单列虽然零遮挡，但已被逼到下限附近（13 字名 → 15px，太小）
    let sol = solve(0, 0);
    if (!sol.ok || sol.fs <= RT_MIN_FS + 1) {
      const bandTopFrac = Math.min(Math.max(rawTop, 0), 0.85);
      const bandH = Math.max(drawH * (HEAD_BAND - bandTopFrac), drawH * 0.10);
      let alt = solve(bandH, 2);
      if (!alt.ok) {                     // 2 列装不下（13 字这种）才退到 3 列
        const a3 = solve(bandH, 3);
        if (a3.ok) alt = a3;
      }
      const muchBigger = alt.ok && (!sol.ok || alt.fs > sol.fs + 2);
      if (muchBigger || (!sol.ok && alt.hits < sol.hits)) sol = alt;
    }

    nameEl.style.maxHeight = sol.cap > 0 ? Math.round(sol.cap) + "px" : "none";
    nameEl.style.width = sol.cw > 0 ? sol.cw + "px" : "";
    nameEl.style.setProperty("--rt-fs", sol.fs.toFixed(1) + "px");
    const plateX = Math.round(sol.left - appRect.left);
    const plateY = Math.round(visTop - appRect.top);
    nameEl.style.left = plateX + "px";
    nameEl.style.top = plateY + "px";
    // 缩放原点 = 立绘**渲染后**盒子的底边中点（双脚），换算到牌子自身的坐标系
    //（牌子的 border-box 左上角就是它的 inline left/top）。二阶段的凑近 / 脉动是
    // CSS 的 scale 属性（img 的原点是 50% 100%），牌子必须绕同一个点放大 —— 否则
    // 8% 的缩放会让牌面往人物身上挪十几像素，“头部左侧空白区”当场失效。
    // ⚠️ 直接量渲染后的 rect，不要用上面那套 trim 反算：反算只要有一项输入过期
    //（trim 还没到、换过图、盒宽与 paint 宽不等），原点就整体偏掉 —— 实测外卖窗口
    // 曾偏 13.5px，牌面放大时肉眼可见地往人物身上靠。量 rect 的话，每次重排都自愈。
    // 牌子自己已经被 scale 放大过，所以必须用未变换的布局位置（plateX/plateY）。
    const ib = img.getBoundingClientRect();
    nameEl.style.setProperty("--rt-ox", (ib.left + ib.width / 2 - (appRect.left + plateX)).toFixed(1) + "px");
    nameEl.style.setProperty("--rt-oy", (ib.bottom - (appRect.top + plateY)).toFixed(1) + "px");
    // 探针读这两个值做“零遮挡 / 字号达标”断言，不写死像素位置
    nameEl.dataset.rtHit = String(sol.hits);
    nameEl.dataset.rtFs = sol.fs.toFixed(1);
    // 定位成功才放出来（CSS 里 .dialog-name 默认 visibility:hidden）
    nameEl.dataset.rt = "1";
    return { left: sol.left, top: visTop, fs: sol.fs, hit: sol.hits };
  }

  // 外卖窗口没有 break 那套 lastLayoutKey / frozenFig 机制，题名单独记一个 key：
  // 几何（立绘盒 + 名字文字）没变就不重算 —— 求解要反复改字号并强制布局，很贵。
  // 定位成功前不算数：图还没解码完时量不到 alpha 剖面，属于“没算出来”而不是
  // “算出没有”，缓存住就永久不重试（休息窗口竖屏实测踩过这个坑）。
  // 注意：这段必须留在“layoutRoleTitle 函数体”之外 —— 两份 js 的同步区间是
  // 从函数内某个锚点同步到函数结尾，放里面会被整段替换删掉（已踩两次）。
  var titleKey = "";
  function layoutRoleTitleCached(figure, box) {
    const nameEl = document.getElementById("dialog-name");
    if (!nameEl) return;
    // trim 是否就绪也要进 key：立绘还没量出透明边距时，题名是按“未补偿”的盒子
    // 算出来的；trim 一到立绘会整体位移（实测外卖窗口偏 13.5px），不重算就一直错位，
    // 连缩放原点都跟着错（牌面放大时肉眼可见地往人物身上靠）。休息窗口的
    // lastLayoutKey 早就把这一项算进去了，这里补齐。
    const key = Math.round(box.left) + "," + Math.round(box.top) + " " +
      Math.round(box.width) + "x" + Math.round(box.height) +
      "|" + (figure.querySelector("img")?.dataset.trim ? "T" : "t") +
      "|" + nameEl.textContent;
    if (key === titleKey && nameEl.dataset.rt) return;
    if (layoutRoleTitle(figure, nameEl, box)) titleKey = key;
  }

  // ---- 对话框内“提醒内容”的统一自适应（短休 / 长休 / 外卖 / 自定义共用）----
  // break.js 与 takeout.js 里的这一段必须逐字相同：四个窗口的提醒内容是一套排版。
  // JS 只负责两件 CSS 做不到的事：
  //   1) 装不下就逐级缩字号（下限 --dlg-fs-min = 15px，再小就看不清了）；
  //   2) 缩到下限仍装不下，让对话框长高（.content.tall），绝不裁字。
  // 文字可用高度的算法见函数内注释；每次调用都先归零再算，结果只取决于窗口尺寸
  // 与文本本身，反复重排不会漂移。
  function cssLenPx(raw, base) {
    var n = parseFloat(raw);
    if (!(n > 0)) return 0;
    var s = String(raw).trim();
    if (/vh$/.test(s)) return (n * (base || window.innerHeight)) / 100;
    if (/vw$/.test(s)) return (n * window.innerWidth) / 100;
    return n;
  }
  function fitDialogText(content) {
    if (!content) return;
    // 取**可见**的那一个 .dialog-text：短休/长休/外卖用 .supervisor-line 说提醒内容，
    // 自定义提醒用 .messages 放自己的提醒内容，两者互斥显示（隐藏的那个高度是 0）。
    // 只取第一个匹配会拿到隐藏的 supervisor-line，直接 return —— 自定义提醒当时
    // 就是这么漏掉的（长文本溢出对话框 20px 没人管）。
    var els = content.querySelectorAll(".dialog-text");
    var el = null;
    for (var i = 0; i < els.length; i++) {
      if (els[i].offsetHeight > 0) { el = els[i]; break; }
    }
    if (!el) return;
    var cst = getComputedStyle(content);
    var padY = parseFloat(cst.paddingTop) + parseFloat(cst.paddingBottom);
    // 文字可用高度 = 对话框应有高度 − 上下内边距。四个窗口共用这一个公式：外卖框里
    // 还有徽标/按钮，它们照样占高，框会自己长高 —— 但那不影响“同一段文字在哪个
    // 窗口里字号都一样”，这正是“保持相同样式”要的（扣掉按钮高度反而会让外卖
    // 的同一句话比休息窗口小 5px，且长文本白缩一次、框照样长高）。
    var boxH = cssLenPx(cst.getPropertyValue("--dlg-h")) || 0.22 * window.innerHeight;
    var budget = boxH - padY;
    el.style.fontSize = "";
    content.classList.remove("tall");
    var minFs = cssLenPx(cst.getPropertyValue("--dlg-fs-min")) || 15;
    var fs = parseFloat(getComputedStyle(el).fontSize) || minFs;
    var guard = 0;
    while (el.offsetHeight > budget + 1 && fs > minFs + 0.01 && guard++ < 40) {
      fs = Math.max(minFs, fs - 1);
      el.style.fontSize = fs + "px";
    }
    if (el.offsetHeight > budget + 1) content.classList.add("tall");
  }

  function layoutSupDialog(figure, content, measure) {
    const container = figure?.parentElement ?? null;
    if (!container || !figure || !content) return;
    const cRect = container.getBoundingClientRect();
    if (cRect.width <= 0) return;
    // 先把提醒内容的字号定下来（内容可能撑高对话框 → 下面所有量测都得在它之后）
    fitDialogText(content);
    const figBox = figure.getBoundingClientRect();
    const vis = figureVisibleRect(figure, figBox.top);
    // 题名与红包区互不影响，先摆它（内部自带 key 缓存，几何没变时直接返回）
    layoutRoleTitleCached(figure, figBox);
    const figLeft = clamp(vis.left - cRect.left, 0, cRect.width);
    const figRight = clamp(vis.right - cRect.left, 0, cRect.width);
    const figTop = clamp(vis.top - cRect.top, 0, cRect.height);
    // 立绘站位已由 CSS 的 --fig-* 变量定死，这里不再碰 figure/content 的定位。
    // 对话框（.content）与窗口边缘的偏移由 CSS 决定（当前贴边为 0），红包区
    // （.tk-redpacket）的包含块就是它——所以摆位全部用实测 rect 换算，不假设贴边。
    // 两个锚点中点（红包“标题+框”整组的中心对准交点，left/bottom 写 inline）：
    //   水平 midX = (立绘可见右缘 + 窗口右缘) / 2
    //   垂直 midY = (立绘可见上缘[去透明] + 底部对话框上边缘) / 2。
    //     即“立绘去透明上缘”与“显示督休专员名称的对话框顶”两线中点。
    //     “外卖提醒”标题胶囊浮在框外上方（.rp-title bottom:100%），框若贴顶
    //     标题会被顶出窗口——两线中点离对话框顶不足（框高+标题高）时，整组
    //     （标题+框）下移到刚好留出顶边距（TOP_MARGIN）为止。figTop/dialogTop
    //     随立绘尺寸/位置/图片动态实测；红包开关关闭时整块 hidden（display:none
    //     不渲染），照常落位无副作用。
    const floatEl = content.querySelector(".tk-redpacket");
    const titleEl = content.querySelector(".rp-title");
    // 包含块换算一律用布局坐标（offsetLeft/offsetTop/offsetHeight）：
    // .content 有入场动画 tk-in（scale 0.97 + translateY），动画进行中
    // getBoundingClientRect 量到的是被 transform 污染的中间态；而动画结束后
    // ResizeObserver 不感知纯 transform 的变化、不会触发重排——过期摆位会一直
    // 留在页面上（实测：水平偏 8px、垂直偏 6px，红包整体错位）。offset* 恒为
    // 终态布局值，动画进行中算出的也是正确落点。督休模式下 .content 是
    // position:fixed（left:0 right:0 bottom:0），offset* 相对视口 = 窗口坐标。
    const boxLeft = content.offsetLeft; // 对话框左缘相对窗口（= 水平内缩量）
    const dialogTop = content.offsetTop; // 对话框顶缘（窗口坐标，= 显示督休专员名称那条边上沿）
    const contentBottom = content.offsetTop + content.offsetHeight; // 对话框底缘（窗口坐标）
    const midX = (figRight + cRect.width) / 2;
    const floatW = (floatEl && floatEl.offsetWidth) || 240;
    const floatH = (floatEl && floatEl.offsetHeight) || 108;
    // 标题悬在框外上方，占“标题高 + margin-bottom”的空间（offsetHeight 不受
    // .content 入场动画的 transform 缩放影响，比 getBoundingClientRect 稳）
    const titleExtra = titleEl
      ? titleEl.offsetHeight + (parseFloat(getComputedStyle(titleEl).marginBottom) || 0)
      : 0;
    const unitH = floatH + titleExtra; // 标题 + 红包框的整组高度
    const TOP_MARGIN = CD_EDGE; // 标题离窗口顶的最小间距
    const midY = Math.max((figTop + dialogTop) / 2, TOP_MARGIN + unitH / 2);
    // 红包框中心 = 单元中心 + titleExtra/2（标题悬在框上方，框心比单元心低半标题）
    let cdX = midX - boxLeft - floatW / 2;
    let cdBottom = contentBottom - midY - titleExtra / 2 - floatH / 2;
    if (floatEl) {
      // clamp 的合法区间是“红包相对对话框”的坐标：窗口坐标的边界先算好再减去内缩量
      cdX = clamp(
        cdX,
        CD_EDGE - boxLeft,
        Math.max(CD_EDGE - boxLeft, cRect.width - boxLeft - floatW - CD_EDGE)
      );
      //   下限：框底不沉入对话框（距对话框底缘 ≥ CD_EDGE）
      //   上限：整组（标题+框）顶离窗口顶 ≥ TOP_MARGIN（标题不出窗）
      cdBottom = clamp(
        cdBottom,
        CD_EDGE,
        Math.max(CD_EDGE, contentBottom - TOP_MARGIN - unitH)
      );
      floatEl.style.left = `${Math.round(cdX)}px`;
      floatEl.style.bottom = `${Math.round(cdBottom)}px`;
      floatEl.style.right = "auto";
    }
    content.dataset.supMetrics = JSON.stringify({
      mode: "dialog",
      figLeft: Math.round(figLeft),
      figRight: Math.round(figRight),
      figTop: Math.round(figTop),
      cdX: Math.round(cdX),
      cdBottom: Math.round(cdBottom),
      midX: Math.round(midX),
      midY: Math.round(midY),
      unitH: Math.round(unitH),
      titleExtra: Math.round(titleExtra),
      boxLeft: Math.round(boxLeft),
      floatW,
      floatH,
      src: figure.querySelector("img")?.dataset.trim ? "trim" : "figure"
    });
  }
  function bindSupContentLayout(figure, content, measure) {
    const relayout = () => layoutSupDialog(figure, content, measure);
    if (!figure || !content) return relayout;
    relayout();
    const container = figure.parentElement;
    if (typeof ResizeObserver !== "undefined") {
      const ro = new ResizeObserver(relayout);
      ro.observe(figure);
      if (container) ro.observe(container);
      if (measure) ro.observe(measure);
      ro.observe(content);
      // 红包框自身尺寸变化（如 .rp-time 内容回流）也要重排，
      // 否则 max-content 宽度变后写死的 left 会让中心偏离交点。
      const floatEl = content.querySelector(".tk-redpacket");
      if (floatEl) ro.observe(floatEl);
    }
    if (measure && typeof MutationObserver !== "undefined") {
      new MutationObserver(relayout).observe(measure, {
        attributes: true,
        attributeFilter: ["style"]
      });
    }
    window.addEventListener("resize", relayout);
    // 题名定位要读立绘的 alpha 剖面，立绘没解码完时量不到 → 那一轮白跑，而
    // ResizeObserver/MutationObserver 未必会再响（figure 高度是 vh 百分比，图片
    // 就绪并不改它）。所以补一串短定时兜底：只在题名还没定位成功时走，成功即停。
    const im = figure.querySelector("img");
    if (im) {
      if (im.complete && im.naturalWidth > 0) relayout();
      else {
        im.addEventListener("load", relayout, { once: true });
        im.addEventListener("error", relayout, { once: true });
      }
    }
    let tries = 0;
    const tick = () => {
      const n = document.getElementById("dialog-name");
      if (n && n.dataset.rt) return;
      if (tries++ >= 16) return;
      relayout();
      window.setTimeout(tick, 80);
    };
    window.setTimeout(tick, 80);
    return relayout;
  }

  // renderer/takeout.ts
  var api = window.api;
  var pad2 = (n) => String(n).padStart(2, "0");
  var el = (id) => document.getElementById(id);
  async function main() {
    const info = await api.takeoutCurrent();
    if (!info) return;
    const app = el("app");
    document.documentElement.style.setProperty("--accent", info.appearance.color);
    document.documentElement.style.setProperty("--app-opacity", String(1 - info.appearance.transparency));
    app.dataset.rounded = info.appearance.fullscreen ? "0" : "1";
    const postEl = el("tk-postponed");
    if (info.postponedCount > 0) {
      postEl.textContent = `已推迟 ${info.postponedCount} 次`;
      postEl.classList.remove("hidden");
    }
    // “推迟时长”最小 1 分钟（2026-11-10），按钮常显，文案带分钟数
    el("tk-postpone").textContent = `推迟 ${info.postponeMin} 分钟`;
    const msgEl = el("tk-msg");
    if (info.message) {
      msgEl.textContent = info.message;
      msgEl.classList.remove("hidden");
    }
    // 红包显隐要先于摆位：layoutSupDialog 量红包的 offsetWidth/Height 定中心，
    // 隐藏状态下量到 0 会走 fallback（位置偏差）。红包显隐不改变 content 尺寸
    // （absolute 脱离文档流），ResizeObserver 不会补触发，顺序必须摆对。
    if (info.redPacketData) {
      el("redpacket").classList.remove("hidden");
      const data = info.redPacketData;
      if (data && data.url) {
        const qr = el("rp-qr");
        qr.classList.remove("hidden");
        el("rp-text").textContent = "外卖优惠券 \xB7 微信扫码领取";
        import_qrcode.default.toCanvas(qr, data.url, { width: 120, margin: 1, color: { dark: "#331a12", light: "#ffffff" } }).catch(() => {
          qr.classList.add("hidden");
          el("rp-text").textContent = "支付红包 \xB7 二维码生成失败";
        });
      }
    }
    if (info.supervisor) {
      msgEl.classList.add("hidden");
      setupSupervisor(info.supervisor, info.message);
      bindSupContentLayout(
        document.getElementById("figure"),
        document.querySelector(".content"),
        document.getElementById("figure-trim")
      );
    }
    const rpTime = el("rp-time");
    const tick = () => {
      const d = /* @__PURE__ */ new Date();
      const t = `${pad2(d.getHours())}:${pad2(d.getMinutes())}:${pad2(d.getSeconds())}`;
      if (rpTime) rpTime.textContent = t;
    };
    tick();
    setInterval(tick, 500);
    const act = (a) => {
      void api.takeoutAction(a);
    };
    for (const [id, action] of [
      ["tk-ordered", "ordered"],
      ["tk-skip", "skip"],
      ["tk-postpone", "postpone"]
    ]) {
      el(id).addEventListener("click", (e) => {
        e.stopPropagation();
        if (info.preview) {
          void api.closeWindow();
          return;
        }
        act(action);
      });
    }
    if (info.preview) {
      el("preview-badge").classList.remove("hidden");
      document.body.addEventListener("click", () => api.closeWindow());
    }
  }
  void main();
})();
