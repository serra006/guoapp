import {Crypto} from 'assets://js/lib/cat.js';

const KEYS = 'd3dGiJc651gSQ8w1';
const CHAR_MAP = {
  '+': 'P', '/': 'X', '0': 'M', '1': 'U', '2': 'l', '3': 'E', '4': 'r', '5': 'Y', '6': 'W', '7': 'b', '8': 'd', '9': 'J',
  'A': '9', 'B': 's', 'C': 'a', 'D': 'I', 'E': '0', 'F': 'o', 'G': 'y', 'H': '_', 'I': 'H', 'J': 'G', 'K': 'i', 'L': 't',
  'M': 'g', 'N': 'N', 'O': 'A', 'P': '8', 'Q': 'F', 'R': 'k', 'S': '3', 'T': 'h', 'U': 'f', 'V': 'R', 'W': 'q', 'X': 'C',
  'Y': '4', 'Z': 'p', 'a': 'm', 'b': 'B', 'c': 'O', 'd': 'u', 'e': 'c', 'f': '6', 'g': 'K', 'h': 'x', 'i': '5', 'j': 'T',
  'k': '-', 'l': '2', 'm': 'z', 'n': 'S', 'o': 'Z', 'p': '1', 'q': 'V', 'r': 'v', 's': 'j', 't': 'Q', 'u': '7', 'v': 'D',
  'w': 'w', 'x': 'n', 'y': 'L', 'z': 'e'
};
const HOST = 'https://api-store.qmplaylet.com';
const READ = 'https://api-read.qmplaylet.com';
const UA = 'okhttp/3.12.11';

function b64(str) {
  return Crypto.enc.Base64.stringify(Crypto.enc.Utf8.parse(str));
}

function md5hex(s) {
  return Crypto.MD5(s).toString(Crypto.enc.Hex).toLowerCase();
}

function buildHeaders() {
  const data = {
    'static_score': '0.8',
    'uuid': '00000000-7fc7-08dc-0000-000000000000',
    'device-id': '20250220125449b9b8cac84c2dd3d035c9052a2572f7dd0122edde3cc42a70',
    'sourceuid': 'aa7de295aad621a6',
    'refresh-type': '0',
    'model': '22021211RC',
    'client-id': 'aa7de295aad621a6',
    'brand': 'Redmi',
    'sys-ver': '12',
    'phone-level': 'H',
    'wlb-uid': 'aa7de295aad621a6',
    'session-id': String(Date.now())
  };
  const b = b64(JSON.stringify(data));
  let qm = '';
  for (const ch of b) qm += CHAR_MAP[ch] || ch;
  const sign = md5hex('AUTHORIZATION=app-version=10001application-id=com.duoduo.read' +
    'channel=unknownis-white=net-env=5platform=androidqm-params=' + qm + 'reg=' + KEYS);
  return {
    'net-env': '5', 'reg': '', 'channel': 'unknown', 'is-white': '',
    'platform': 'android', 'application-id': 'com.duoduo.read',
    'authorization': '', 'app-version': '10001', 'user-agent': 'webviewversion/0',
    'qm-params': qm, 'sign': sign
  };
}

async function http(url, headers) {
  const h = Object.assign({ 'User-Agent': UA, 'content-type': 'application/json; charset=utf-8' }, headers || {});
  const res = await req(url, { headers: h, timeout: 20000 });
  return JSON.parse(res.content);
}

function mapItem(i) {
  return {
    vod_id: String(i.playlet_id || ''),
    vod_name: i.title || '',
    vod_pic: i.image_link || '',
    vod_remarks: (i.total_episode_num ? i.total_episode_num + '集' : '')
  };
}

export function __jsEvalReturn() {
  return {
    init: async function () { return true; },

    home: async function () {
      return JSON.stringify({
        class: [{ type_id: '0', type_name: '全部短剧' }],
        filters: {}
      });
    },

    homeVod: async function () {
      const sign = md5hex('operation=1playlet_privacy=1tag_id=0' + KEYS);
      const res = await http(HOST + '/api/v1/playlet/index?tag_id=0&playlet_privacy=1&operation=1&sign=' + sign, buildHeaders());
      const list = (res.data && res.data.list) || [];
      return JSON.stringify({ list: list.map(mapItem) });
    },

    category: async function (tid, pg) {
      const page = parseInt(pg) || 1;
      const tag = String(tid || '0');
      const sign = md5hex('operation=1playlet_privacy=1tag_id=' + tag + KEYS);
      let url = HOST + '/api/v1/playlet/index?tag_id=' + tag + '&playlet_privacy=1&operation=1&sign=' + sign;
      if (page > 1) url += '&page=' + page;
      const res = await http(url, buildHeaders());
      const list = (res.data && res.data.list) || [];
      return JSON.stringify({
        list: list.map(mapItem),
        page: page,
        pagecount: page + 1,
        limit: list.length,
        total: list.length * (page + 1)
      });
    },

    detail: async function (id) {
      const pid = String(id);
      const sign = md5hex('playlet_id=' + pid + KEYS);
      const res = await http(READ + '/player/api/v1/playlet/info?playlet_id=' + pid + '&sign=' + sign, buildHeaders());
      const d = res.data || {};
      const pl = d.play_list || [];
      const eps = pl.map(function (i) {
        return (i.sort || '') + '$' + (i.video_url || '');
      }).join('#');
      return JSON.stringify({
        list: [{
          vod_id: pid,
          vod_name: d.title || '',
          vod_pic: d.image_link || '',
          vod_remarks: (d.total_episode_num ? d.total_episode_num + '集' : ''),
          vod_content: d.intro || '',
          vod_play_from: '七猫短剧',
          vod_play_url: eps
        }]
      });
    },

    play: async function (flag, id) {
      return JSON.stringify({
        parse: 0,
        url: id,
        header: { 'User-Agent': UA }
      });
    },

    search: async function (wd) {
      const kw = String(wd || '');
      const sign = md5hex('operation=2playlet_privacy=1search_word=' + kw + KEYS);
      const res = await http(HOST + '/api/v1/playlet/search?search_word=' + encodeURIComponent(kw) + '&playlet_privacy=1&operation=2&sign=' + sign, buildHeaders());
      const arr = (res.data && res.data.list) || [];
      const list = Array.isArray(arr) ? arr.map(mapItem) : [];
      return JSON.stringify({ list: list });
    }
  };
}
