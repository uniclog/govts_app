import { memo, useEffect, useState } from "react";
import { request } from "../../api/http";
import { Icon } from "../../components/Icon";

interface AttachmentInfo { name: string; size: number }

export const Attachment = memo(function Attachment({ id }: { id: string }) {
  const [info, setInfo] = useState<AttachmentInfo>();
  const [preview, setPreview] = useState(true);
  const url = "/api/files/attachments/" + id;
  useEffect(() => {
    const controller = new AbortController();
    void request<AttachmentInfo>(url + "/info", { signal: controller.signal })
      .then((item) => { if (!controller.signal.aborted) setInfo(item); })
      .catch(() => undefined);
    return () => controller.abort();
  }, [url]);
  const image = info && /\.(?:jpe?g|png|webp)$/i.test(info.name);
  const size = info ? new Intl.NumberFormat("ru", { style: "unit", unit: "kilobyte", maximumFractionDigits: 1 }).format(info.size / 1024) : "";
  return <a className="message-file" href={url} download>
    {image && preview && <img loading="lazy" src={url + "/preview"} alt="" onError={() => setPreview(false)} />}
    <span><Icon name="attachment" />{info?.name ?? "Скачать вложение"}{size && <small>{size}</small>}</span>
  </a>;
});
