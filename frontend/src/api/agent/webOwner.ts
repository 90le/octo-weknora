import { ref } from 'vue';

interface WebOwnerApi {
  get: (id: string) => Promise<{ bound: boolean }>;
  bind: (id: string) => Promise<unknown>;
  unbind: (id: string) => Promise<unknown>;
}

export function useWebOwnerBinding(api: WebOwnerApi) {
  const bound = ref<boolean | null>(null);
  const loading = ref(false);
  const error = ref('');
  let version = 0;

  function reset() {
    version++;
    bound.value = null;
    loading.value = false;
    error.value = '';
  }

  async function read(id: string) {
    const result = await api.get(id);
    if (typeof result?.bound !== 'boolean') throw new Error('Invalid binding status');
    return result.bound;
  }

  async function load(id: string) {
    const request = ++version;
    bound.value = null;
    error.value = '';
    loading.value = true;
    try {
      const value = await read(id);
      if (request === version) bound.value = value;
    } catch {
      if (request === version) error.value = '无法读取绑定状态，请重试。';
    } finally {
      if (request === version) loading.value = false;
    }
  }

  async function change(id: string, desired: boolean) {
    if (loading.value || bound.value === null || bound.value === desired) return false;
    const request = ++version;
    loading.value = true;
    error.value = '';
    try {
      await (desired ? api.bind(id) : api.unbind(id));
      const value = await read(id);
      if (request !== version) return false;
      bound.value = value;
      if (value !== desired) {
        error.value = '操作后状态未改变，请重试。';
        return false;
      }
      return true;
    } catch {
      if (request === version) {
        bound.value = null;
        error.value = '操作或状态核验失败，结果未知，请重新读取状态。';
      }
      return false;
    } finally {
      if (request === version) loading.value = false;
    }
  }

  return { bound, loading, error, reset, load, change };
}
